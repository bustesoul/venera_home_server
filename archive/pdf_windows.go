//go:build windows

package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	backendpkg "venera_home_server/backend"
	"venera_home_server/shared"
)

type pdfArchive struct {
	sourcePath string
	renderDir  string
	entries    []ArchiveEntry
	cacheDir   string
}

type pdfInfo struct {
	PageCount int `json:"page_count"`
}

func openPDFArchive(ctx context.Context, backend backendpkg.Backend, rel, cacheDir string) (Archive, error) {
	sourcePath, key, _, err := materializeArchiveSource(ctx, backend, rel, cacheDir)
	if err != nil {
		return nil, err
	}
	renderDir := filepath.Join(cacheDir, "pdf", key)
	info, err := inspectPDF(ctx, cacheDir, sourcePath, filepath.Join(renderDir, "info.json"))
	if err != nil {
		return nil, err
	}
	entries := make([]ArchiveEntry, 0, info.PageCount)
	for i := 1; i <= info.PageCount; i++ {
		entries = append(entries, ArchiveEntry{Name: fmt.Sprintf("%04d.png", i)})
	}
	return &pdfArchive{sourcePath: sourcePath, renderDir: renderDir, entries: entries, cacheDir: cacheDir}, nil
}

func (a *pdfArchive) Format() string { return "pdf" }
func (a *pdfArchive) Entries() []ArchiveEntry {
	return append([]ArchiveEntry(nil), a.entries...)
}
func (a *pdfArchive) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	pageIndex, err := parsePDFPageName(name)
	if err != nil {
		return nil, err
	}
	outPath := filepath.Join(a.renderDir, "pages", fmt.Sprintf("%04d.png", pageIndex+1))
	if _, err := os.Stat(outPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := renderPDFPage(ctx, a.cacheDir, a.sourcePath, outPath, pageIndex); err != nil {
			return nil, err
		}
	} else {
		_ = shared.TouchFile(outPath)
	}
	return os.Open(outPath)
}
func (a *pdfArchive) Close() error { return nil }

func parsePDFPageName(name string) (int, error) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	value, err := strconv.Atoi(base)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid pdf page name: %s", name)
	}
	return value - 1, nil
}

func inspectPDF(ctx context.Context, cacheDir, sourcePath, infoPath string) (*pdfInfo, error) {
	if raw, err := os.ReadFile(infoPath); err == nil {
		var info pdfInfo
		if json.Unmarshal(raw, &info) == nil && info.PageCount > 0 {
			_ = shared.TouchFile(infoPath)
			return &info, nil
		}
	}
	output, err := runPDFScript(ctx, cacheDir, "-Action", "inspect", "-SourcePath", sourcePath)
	if err != nil {
		return nil, err
	}
	var info pdfInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return nil, fmt.Errorf("parse pdf info: %w", err)
	}
	if info.PageCount <= 0 {
		return nil, fmt.Errorf("pdf has no pages")
	}
	if err := shared.EnsureDir(filepath.Dir(infoPath)); err == nil {
		if raw, err := json.Marshal(info); err == nil {
			_ = os.WriteFile(infoPath, raw, 0o644)
		}
	}
	return &info, nil
}

func renderPDFPage(ctx context.Context, cacheDir, sourcePath, outputPath string, pageIndex int) error {
	_, err := runPDFScript(ctx, cacheDir, "-Action", "render", "-SourcePath", sourcePath, "-OutputPath", outputPath, "-PageIndex", strconv.Itoa(pageIndex))
	return err
}

func runPDFScript(ctx context.Context, cacheDir string, args ...string) ([]byte, error) {
	scriptPath, err := ensurePDFScript(cacheDir)
	if err != nil {
		return nil, err
	}
	commandArgs := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "powershell.exe", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("powershell pdf helper: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func ensurePDFScript(cacheDir string) (string, error) {
	scriptPath := filepath.Join(cacheDir, "pdf", "pdf_tools.ps1")
	if _, err := os.Stat(scriptPath); err == nil {
		return scriptPath, nil
	}
	if err := shared.EnsureDir(filepath.Dir(scriptPath)); err != nil {
		return "", err
	}
	script := `param(
    [ValidateSet("inspect","render")]
    [string]$Action,
    [string]$SourcePath,
    [string]$OutputPath = "",
    [int]$PageIndex = 0
)
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null = [Windows.Storage.StorageFile, Windows.Storage, ContentType=WindowsRuntime]
$null = [Windows.Data.Pdf.PdfDocument, Windows.Data.Pdf, ContentType=WindowsRuntime]
$null = [Windows.Storage.Streams.InMemoryRandomAccessStream, Windows.Storage.Streams, ContentType=WindowsRuntime]
$null = [Windows.Storage.Streams.DataReader, Windows.Storage.Streams, ContentType=WindowsRuntime]
$asTaskGen = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and $_.IsGenericMethod -and $_.GetParameters().Count -eq 1 })[0]
$asTask = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and -not $_.IsGenericMethod -and $_.GetParameters().Count -eq 1 })[0]
$fileTask = $asTaskGen.MakeGenericMethod([Windows.Storage.StorageFile]).Invoke($null, @([Windows.Storage.StorageFile]::GetFileFromPathAsync($SourcePath)))
$file = $fileTask.Result
$pdfTask = $asTaskGen.MakeGenericMethod([Windows.Data.Pdf.PdfDocument]).Invoke($null, @([Windows.Data.Pdf.PdfDocument]::LoadFromFileAsync($file)))
$pdf = $pdfTask.Result
if ($Action -eq "inspect") {
    @{ page_count = [int]$pdf.PageCount } | ConvertTo-Json -Compress
    exit 0
}
$page = $pdf.GetPage([uint32]$PageIndex)
$stream = New-Object Windows.Storage.Streams.InMemoryRandomAccessStream
$renderTask = $asTask.Invoke($null, @($page.RenderToStreamAsync($stream)))
$renderTask.Wait()
$stream.Seek(0)
$reader = New-Object Windows.Storage.Streams.DataReader($stream.GetInputStreamAt(0))
$loadTask = $asTaskGen.MakeGenericMethod([uint32]).Invoke($null, @($reader.LoadAsync([uint32]$stream.Size)))
[void]$loadTask.Result
$bytes = New-Object byte[] ([int]$stream.Size)
$reader.ReadBytes($bytes)
[System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($OutputPath)) | Out-Null
[System.IO.File]::WriteAllBytes($OutputPath, $bytes)
Write-Output $OutputPath
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return "", err
	}
	return scriptPath, nil
}
