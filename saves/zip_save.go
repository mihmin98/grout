package saves

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ZipDirectory creates a zip file from a single directory.
// Returns the path to the temporary zip file. Caller is responsible for cleanup.
func ZipDirectory(dirPath string) (string, error) {
	return ZipDirectories([]string{dirPath})
}

// ZipDirectories creates a zip file containing multiple directories.
// Each directory is preserved as a top-level subdirectory in the archive,
// allowing a single zip to hold e.g. UCUS98751_DATA00/, UCUS98751_DATA01/, UCUS98751_INSDIR/.
// Returns the path to the temporary zip file. Caller is responsible for cleanup.
func ZipDirectories(dirPaths []string) (string, error) {
	tmpFile, err := os.CreateTemp("", "grout-save-*.zip")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	w := zip.NewWriter(tmpFile)
	defer w.Close()

	for _, dirPath := range dirPaths {
		if err := addDirToZip(w, dirPath); err != nil {
			os.Remove(tmpFile.Name())
			return "", err
		}
	}

	return tmpFile.Name(), nil
}

// addDirToZip walks dirPath and writes all its contents into w,
// preserving the directory's base name as the top-level entry in the archive.
func addDirToZip(w *zip.Writer, dirPath string) error {
	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(filepath.Dir(dirPath), path)
		if err != nil {
			return err
		}

		if strings.HasPrefix(filepath.Base(rel), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			_, err := w.Create(rel + "/")
			return err
		}

		fw, err := w.Create(rel)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(fw, f)
		return err
	})
}

// UnzipToDirectory extracts a zip file to a target directory.
func UnzipToDirectory(zipPath, targetDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		targetPath := filepath.Join(targetDir, f.Name)

		// Prevent zip slip
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}
	}

	return nil
}

// ZipSaveUnit writes a flat zip of a multi-file save into a fresh temp folder,
// named <stem of primary>.zip, entries named after the files on disk with the
// primary first. Returns the zip path and a cleanup func that removes the folder.
// The name matters: the upload sends the file under its own name, and Argosy
// unpacks a <stem>.zip of <stem>.srm + <stem>.rtc.
func ZipSaveUnit(primary string, companions []string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "grout-save-unit-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	base := filepath.Base(primary)
	zipPath := filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+".zip")
	if err := writeFlatZip(zipPath, append([]string{primary}, companions...)); err != nil {
		cleanup()
		return "", nil, err
	}
	return zipPath, cleanup, nil
}

// writeFlatZip writes the files into a new zip at zipPath, in order, each under
// its base name with its modification time.
func writeFlatZip(zipPath string, paths []string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	w := zip.NewWriter(out)
	for _, p := range paths {
		if err := addFileToZip(w, p); err != nil {
			w.Close()
			out.Close()
			return err
		}
	}
	if err := w.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func addFileToZip(w *zip.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	fw, err := w.CreateHeader(&zip.FileHeader{
		Name:     filepath.Base(path),
		Method:   zip.Deflate,
		Modified: info.ModTime(),
	})
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, f)
	return err
}
