package shell

import "testing"

func TestRenderCLIXMLLineDecodesErrorStreamAndCollapsesNativeBanner(t *testing.T) {
	// Captured from Windows PowerShell 5.1: `cmd /c "echo warn 1>&2" 2>&1 | Out-Host` with a piped stderr.
	line := `<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<S S="Error">cmd : warn _x000D__x000A_</S>` +
		`<S S="Error">At line:1 char:1_x000D__x000A_</S>` +
		`<S S="Error">+ cmd /c "echo warn 1&gt;&amp;2" 2&gt;&amp;1 | Out-Host_x000D__x000A_</S>` +
		`<S S="Error">+ ~~~~~~~~~~~~~~~~~~~~~~~~~~~~_x000D__x000A_</S>` +
		`<S S="Error">    + CategoryInfo          : NotSpecified: (warn :String) [], RemoteException_x000D__x000A_</S>` +
		`<S S="Error">    + FullyQualifiedErrorId : NativeCommandError_x000D__x000A_</S>` +
		`<S S="Error"> _x000D__x000A_</S>` +
		`<S S="Error">second line_x000D__x000A_</S></Objs>`
	text, keep := renderCLIXMLLine(line)
	if !keep || text != "warn \nsecond line" {
		t.Fatalf("got %q keep=%v", text, keep)
	}
}

func TestNativeErrorBannerFilterDropsPlainTextBanner(t *testing.T) {
	run := func(input ...string) []string {
		filter := &nativeErrorBannerFilter{}
		var out []string
		for _, line := range input {
			out = append(out, filter.push(line)...)
		}
		return append(out, filter.flush()...)
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// Captured from a nested powershell.exe whose stdout is a pipe.
	got := run("cmd : first  ", "At line:1 char:1", `+ cmd /c "echo first 1>&2" 2>&1 | Out-Host`,
		"+ ~~~~~~~~", "    + CategoryInfo          : NotSpecified: (first  :String) [], RemoteException",
		"    + FullyQualifiedErrorId : NativeCommandError", " ", "second ")
	if want := []string{"cmd : first  ", "second "}; !equal(got, want) {
		t.Fatalf("native banner: got %q want %q", got, want)
	}
	// The same banner relayed with an empty line after every line.
	got = run("cmd : first  ", "", "At line:1 char:1", "", "+ cmd", "", "+ ~~~", "",
		"    + CategoryInfo          : NotSpecified", "", "    + FullyQualifiedErrorId : NativeCommandError",
		"", " ", "", "second ")
	if want := []string{"cmd : first  ", "", "", "", "second "}; !equal(got, want) {
		t.Fatalf("relayed native banner: got %q want %q", got, want)
	}

	// A non-native error banner and an "At" line without a preceding " : " line pass unchanged.
	other := []string{"Write-Error : boom", "At line:1 char:1", "+ Write-Error boom",
		"    + FullyQualifiedErrorId : Microsoft.PowerShell.Commands.WriteErrorException", "next"}
	if got := run(other...); !equal(got, other) {
		t.Fatalf("non-native banner changed: %q", got)
	}
	plain := []string{"hello", "At line:1 char:1", "+ text"}
	if got := run(plain...); !equal(got, plain) {
		t.Fatalf("plain lines changed: %q", got)
	}
}

func TestRenderCLIXMLLineKeepsOrdinaryErrorsAndDropsNoise(t *testing.T) {
	text, keep := renderCLIXMLLine(`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<S S="Error">Write-Error : boom_x000D__x000A_</S>` +
		`<S S="Error">At line:1 char:1_x000D__x000A_</S>` +
		`<S S="Error">+ Write-Error boom_x000D__x000A_</S>` +
		`<S S="Error">    + FullyQualifiedErrorId : Microsoft.PowerShell.Commands.WriteErrorException_x000D__x000A_</S></Objs>`)
	want := "Write-Error : boom\nAt line:1 char:1\n+ Write-Error boom\n    + FullyQualifiedErrorId : Microsoft.PowerShell.Commands.WriteErrorException"
	if !keep || text != want {
		t.Fatalf("non-native error should keep its view: %q", text)
	}
	if _, keep := renderCLIXMLLine("#< CLIXML"); keep {
		t.Fatal("marker line should be dropped")
	}
	if _, keep := renderCLIXMLLine(`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T></TN><MS><I64 N="SourceId">1</I64></MS></Obj></Objs>`); keep {
		t.Fatal("progress-only block should be dropped")
	}
	for _, plain := range []string{"<Objs>not clixml", "hello _x000D_ world", `<Objs xmlns="http://schemas.microsoft.com/powershell/2004/04"><S S="Error">cut`} {
		if text, keep := renderCLIXMLLine(plain); !keep || text != plain {
			t.Fatalf("%q changed to %q", plain, text)
		}
	}
}
