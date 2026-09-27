//go:build windows

package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// authenticodeScript reads the file path from UT_VERIFY_FILE and never has it
// spliced into its text. It prints one JSON line for checkAuthenticodeResult.
const authenticodeScript = `$ErrorActionPreference = 'Stop'
$s = Get-AuthenticodeSignature -LiteralPath $env:UT_VERIFY_FILE
$signer = ''
$subject = ''
if ($s.SignerCertificate) {
  $signer = $s.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
  $subject = $s.SignerCertificate.Subject
}
[pscustomobject]@{ status = $s.Status.ToString(); signer = $signer; subject = $subject } | ConvertTo-Json -Compress`

// queryAuthenticode asks Windows (WinVerifyTrust, through
// Get-AuthenticodeSignature) for path's signature status and signer.
func queryAuthenticode(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, powershellExe(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", authenticodeScript)
	cmd.Env = append(os.Environ(), "UT_VERIFY_FILE="+path)
	configureHidden(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Get-AuthenticodeSignature: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
