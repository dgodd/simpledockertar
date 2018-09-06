package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	httpc := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", "/var/run/docker.sock")
			},
		},
	}

	res, err := httpc.Get("http://unix/images/ubuntu:18.04/get")
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		panic(fmt.Sprintf("expected 200: actual: %d", res.StatusCode))
	}

	r, w := io.Pipe()
	tarball := tar.NewWriter(w)

	go func() {
		tarReader := tar.NewReader(res.Body)
		var parentLayerID string
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				panic(err)
			}

			if !(strings.HasSuffix(header.Name, "/VERSION") || strings.HasSuffix(header.Name, "/json") || strings.HasSuffix(header.Name, "/layer.tar")) {
				continue
			}

			fmt.Println(header.Name, header.FileInfo())

			if err := tarball.WriteHeader(header); err != nil {
				panic(err)
			}
			if _, err = io.Copy(tarball, tarReader); err != nil {
				panic(err)
			}

			parentLayerID = strings.Split(header.Name, "/")[0]
		}

		for _, name := range []string{"app", "config", "sh.packs.samples.buildpack.nodejs/nodejs", "sh.packs.samples.buildpack.nodejs/node_modules"} {
			b, err := tarDir(filepath.Join("/tmp/pack.build.033897531", name), "launch/"+name)
			if err != nil {
				panic(err)
			}
			layerID := fmt.Sprintf("%x", sha256.Sum256(b))
			addFileToTar(tarball, layerID+"/VERSION", []byte("1.0"))
			addFileToTar(tarball, layerID+"/json", []byte(fmt.Sprintf(`{"id":"%s","parent":"%s","os":"linux"}`, layerID, parentLayerID)))
			addFileToTar(tarball, layerID+"/layer.tar", b)
			parentLayerID = layerID
		}

		if err := addFileToTar(tarball, "repositories", []byte(fmt.Sprintf(`{"dave2":{"latest":"%s"}}`, parentLayerID))); err != nil {
			panic(err)
		}

		tarball.Close()
		w.Close()
	}()

	res, err = httpc.Post("http://unix/images/load", "application/tar", r)
	r.Close()
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		panic(fmt.Sprintf("expected 200: actual: %d", res.StatusCode))
	}
	fmt.Printf("POST: %#v\n", res)
	io.Copy(os.Stdout, res.Body)
}

func addFileToTar(w *tar.Writer, path string, contents []byte) error {
	if err := w.WriteHeader(&tar.Header{Name: path, Size: int64(len(contents))}); err != nil {
		return err
	}
	_, err := w.Write([]byte(contents))
	return err
}

func tarDir(srcDir, destDir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		destPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		destPath = filepath.Join(destDir, destPath)
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			fmt.Println("SYMLINK:", destPath)
			// TODO handle correctly
			return nil
		}

		if err := tw.WriteHeader(&tar.Header{
			Name:    destPath,
			Size:    info.Size(),
			Mode:    int64(info.Mode()),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}

		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fh.Close()
		_, err = io.Copy(tw, fh)
		return err
	})
	tw.Close()
	return buf.Bytes(), err
}
