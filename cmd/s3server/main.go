package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	storageRoot := "/tmp/s3-storage"
	_ = os.MkdirAll(storageRoot, 0777)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cleanPath := filepath.Clean(strings.Trim(r.URL.Path, "/"))
		target := filepath.Join(storageRoot, cleanPath)

		switch r.Method {
		case http.MethodPut:
			if strings.Count(cleanPath, string(filepath.Separator)) == 0 && cleanPath != "." {
				// Bucket creation
				_ = os.MkdirAll(target, 0777)
				w.WriteHeader(http.StatusOK)
				return
			}
			_ = os.MkdirAll(filepath.Dir(target), 0777)
			f, err := os.Create(target)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
			_, err = io.Copy(f, r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			log.Printf("[S3] Stored object %s", cleanPath)

		case http.MethodGet:
			fi, err := os.Stat(target)
			if err == nil && fi.IsDir() {
				// S3 ListObjects XML
				prefix := r.URL.Query().Get("prefix")
				var keys []string
				_ = filepath.Walk(target, func(p string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() {
						rel, _ := filepath.Rel(target, p)
						relKey := filepath.ToSlash(rel)
						if prefix == "" || strings.HasPrefix(relKey, prefix) {
							keys = append(keys, relKey)
						}
					}
					return nil
				})
				var b strings.Builder
				b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
				b.WriteString("<ListBucketResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">\n")
				b.WriteString(fmt.Sprintf("  <Name>%s</Name>\n", cleanPath))
				b.WriteString(fmt.Sprintf("  <Prefix>%s</Prefix>\n", prefix))
				for _, k := range keys {
					b.WriteString(fmt.Sprintf("  <Contents><Key>%s</Key></Contents>\n", k))
				}
				b.WriteString("</ListBucketResult>")
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(b.String()))
				log.Printf("[S3] ListObjects %s prefix=%s count=%d", cleanPath, prefix, len(keys))
				return
			}

			f, err := os.Open(target)
			if err != nil {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, f)
			log.Printf("[S3] Served object %s", cleanPath)

		case http.MethodDelete:
			_ = os.Remove(target)
			w.WriteHeader(http.StatusNoContent)
			log.Printf("[S3] Deleted object %s", cleanPath)

		case http.MethodHead:
			if _, err := os.Stat(target); err == nil {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}

		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	port := ":9000"
	fmt.Printf("MatrixDB S3 Object Storage listening on %s (root: %s)\n", port, storageRoot)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("S3 server error: %v", err)
	}
}
