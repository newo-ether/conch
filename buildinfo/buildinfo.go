package buildinfo

import "fmt"

var (
	Version   = "dev"
	Revision  = "unknown"
	BuildTime = "unknown"
)

const ProtocolVersion = "1"

var Capabilities = []string{
	"atomic_file_edit",
	"durable_shell_jobs",
	"encrypted_sse_v1",
	"expected_sha256",
	"job_ack",
	"key_refresh",
}

type Metadata struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Revision        string   `json:"revision"`
	BuildTime       string   `json:"build_time"`
	ProtocolVersion string   `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
}

func Current(name string) Metadata {
	capabilities := append([]string(nil), Capabilities...)
	return Metadata{
		Name:            name,
		Version:         Version,
		Revision:        Revision,
		BuildTime:       BuildTime,
		ProtocolVersion: ProtocolVersion,
		Capabilities:    capabilities,
	}
}

func String(name string) string {
	return fmt.Sprintf(
		"%s %s (revision %s, built %s, protocol %s)",
		name,
		Version,
		Revision,
		BuildTime,
		ProtocolVersion,
	)
}
