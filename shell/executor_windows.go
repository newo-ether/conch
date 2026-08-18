//go:build windows

package shell

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	getConsoleOutputCodePage = kernel32.NewProc("GetConsoleOutputCP")
	getWindowsAnsiCodePage   = kernel32.NewProc("GetACP")
	getWindowsOemCodePage    = kernel32.NewProc("GetOEMCP")
	multiByteToWideChar      = kernel32.NewProc("MultiByteToWideChar")
)

const (
	utf8CodePage              = 65001
	simplifiedChineseCodePage = 936
	multiByteErrInvalidChars  = 0x00000008
)

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	// Windows PowerShell 5.1 treats `-Command -` as an interactive stdin session: a complete legal
	// here-string can reach EOF without execution and syntax errors can become exit-0 empty output.
	// Keep only this fixed bootstrap on the command line. It reads the complete caller source as
	// UTF-8, parses it once, and invokes it. Source therefore retains the public 64 KiB bound without
	// exceeding Windows' ~32K UTF-16 command-line limit or being persisted in a temporary file.
	//
	// All streams are merged through one pipeline (*>&1) and re-emitted as plain text. When the
	// stderr handle is a pipe, PowerShell 5.1 serializes error/warning records as CLIXML XML with
	// _xHHHH_ escapes; rendering each record with Out-String and writing it through [Console] keeps
	// stderr human-readable. Native-command stderr lines surface as ErrorRecords under 2>&1 and are
	// rendered the same way. A terminating error is caught so its text stays plain and the process
	// exits 1, matching the exit code of an uncaught throw.
	const bootstrap = "$ProgressPreference = 'SilentlyContinue'\n" +
		"[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)\n" +
		"[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)\n" +
		"$OutputEncoding = [Console]::OutputEncoding\n" +
		"$reader = [IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.UTF8Encoding]::new($false), $true)\n" +
		"try { $source = $reader.ReadToEnd() } finally { $reader.Dispose() }\n" +
		"$script = [ScriptBlock]::Create($source)\n" +
		"try {\n" +
		"  & $script *>&1 | ForEach-Object {\n" +
		"    if ($_ -is [System.Management.Automation.ErrorRecord] -or $_ -is [System.Management.Automation.WarningRecord] -or $_ -is [System.Management.Automation.VerboseRecord] -or $_ -is [System.Management.Automation.DebugRecord]) {\n" +
		"      [Console]::Error.Write(($_ | Out-String))\n" +
		"    } else {\n" +
		"      [Console]::Out.Write(($_ | Out-String))\n" +
		"    }\n" +
		"  }\n" +
		"} catch {\n" +
		"  [Console]::Error.Write(($_ | Out-String))\n" +
		"  exit 1\n" +
		"}\n" +
		"if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }\n"
	cmd := exec.CommandContext(
		ctx,
		"powershell",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-EncodedCommand",
		encodePowerShellCommand(bootstrap),
	)
	cmd.Stdin = strings.NewReader(command)
	return cmd
}

func encodePowerShellCommand(command string) string {
	units := utf16.Encode([]rune(command))
	bytes := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(bytes[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func decodeShellOutputLine(line []byte) string {
	if utf8.Valid(line) {
		return string(line)
	}

	// Existing installations and native programs can still emit the process/console code page.
	// Decode invalid UTF-8 before it reaches JSON. CP936 is an explicit final candidate because a
	// Conch service can have no attached console (GetConsoleOutputCP then returns zero) while the
	// system ANSI code page remains GBK.
	candidates := []uintptr{
		callCodePage(getConsoleOutputCodePage),
		callCodePage(getWindowsAnsiCodePage),
		callCodePage(getWindowsOemCodePage),
		simplifiedChineseCodePage,
	}
	seen := map[uintptr]bool{0: true, utf8CodePage: true}
	for _, codePage := range candidates {
		if seen[codePage] {
			continue
		}
		seen[codePage] = true
		if decoded, ok := decodeWindowsCodePage(line, codePage); ok {
			return decoded
		}
	}
	return strings.ToValidUTF8(string(line), "\uFFFD")
}

func callCodePage(proc *syscall.LazyProc) uintptr {
	value, _, _ := proc.Call()
	return value
}

func decodeWindowsCodePage(input []byte, codePage uintptr) (string, bool) {
	if len(input) == 0 {
		return "", true
	}
	needed, _, _ := multiByteToWideChar.Call(
		codePage,
		multiByteErrInvalidChars,
		uintptr(unsafe.Pointer(&input[0])),
		uintptr(len(input)),
		0,
		0,
	)
	if needed == 0 {
		return "", false
	}
	output := make([]uint16, int(needed))
	written, _, _ := multiByteToWideChar.Call(
		codePage,
		multiByteErrInvalidChars,
		uintptr(unsafe.Pointer(&input[0])),
		uintptr(len(input)),
		uintptr(unsafe.Pointer(&output[0])),
		uintptr(len(output)),
	)
	if written == 0 {
		return "", false
	}
	return string(utf16.Decode(output[:int(written)])), true
}

func setSysProcAttr(cmd *exec.Cmd) {
	// The process is attached to a Job Object immediately after Start, when its handle exists.
}

type windowsProcessTreeController struct {
	cmd *exec.Cmd
	job windows.Handle
}

func newProcessTreeController(cmd *exec.Cmd) (processTreeController, error) {
	controller := &windowsProcessTreeController{cmd: cmd}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return controller, err
	}
	controller.job = job

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		controller.job = 0
		return controller, err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		windows.CloseHandle(job)
		controller.job = 0
		return controller, err
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		controller.job = 0
		return controller, err
	}
	return controller, nil
}

func (c *windowsProcessTreeController) Kill() {
	if c.job != 0 {
		if err := windows.TerminateJobObject(c.job, 1); err == nil {
			return
		}
	}
	if c.cmd.Process != nil {
		// Compatibility fallback for hosts that disallow nested Job Objects.
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(c.cmd.Process.Pid)).Run()
	}
}

func (c *windowsProcessTreeController) Close() {
	if c.job == 0 {
		return
	}
	// Preserve the historical ability for an explicitly detached child to survive a successful
	// command. Clearing KILL_ON_JOB_CLOSE is safe here because cmd.Wait and both pipe readers have
	// completed; on cancellation Kill runs before this method. If Conch itself crashes before
	// normal completion, the still-open Job Object kills the orphaned tree.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	_, _ = windows.SetInformationJobObject(
		c.job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	_ = windows.CloseHandle(c.job)
	c.job = 0
}
