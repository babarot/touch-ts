package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/barasher/go-exiftool"
	"github.com/jessevdk/go-flags"
	"github.com/jinzhu/now"
	"github.com/k0kubun/go-ansi"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

type Option struct {
	Timestamp string `short:"t" long:"ts"      description:"Timestamp"                                                                                                 required:"false" default:"now"`
	Dir       string `short:"d" long:"dir"     description:"Directory holding photo files"                                                                             required:"false" default:"."`
	Dryrun    bool   `short:"n" long:"dry-run" description:"Displays the operations that would be performed using the specified command without actually running them" required:"false"`
}

type Photo struct {
	Name     string
	Metadata exiftool.FileMetadata
}

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] failed to rename files: %v\n", err)
		os.Exit(1)
	}
}

func runMain() error {
	ctx := context.Background()

	var opt Option
	args, err := flags.Parse(&opt)
	if err != nil {
		return err
	}

	var paths []string
	for _, arg := range args {
		// walkDir can traverse dirs or files
		files, err := walkDir(arg)
		if err != nil {
			return err
		}
		paths = append(paths, files...)
	}

	photos, err := touch(ctx, paths, opt.Timestamp, opt.Dryrun)
	if err != nil {
		return err
	}

	for _, photo := range photos {
		fmt.Println("done", photo)
	}

	return nil
}

func walkDir(root string) ([]string, error) {
	files := []string{}

	err := filepath.WalkDir(root, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		files = append(files, path)
		return nil
	})

	return files, err
}

func touch(ctx context.Context, files []string, datetime string, dryrun bool) ([]Photo, error) {
	ch := make(chan Photo)
	eg, ctx := errgroup.WithContext(ctx)

	for _, file := range files {
		file := file
		eg.Go(func() error {
			photo, err := modify(file, datetime, dryrun)
			if err != nil {
				return fmt.Errorf("%s: failed to get EXIF data: %w", file, err)
			}
			select {
			case ch <- photo:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}

	go func() {
		// do not handle error at this time
		// because it would be done at the end of this func
		_ = eg.Wait()
		close(ch)
	}()

	bar := progressbar.NewOptions(len(files),
		progressbar.OptionSetWriter(ansi.NewAnsiStdout()),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetWidth(20),
		progressbar.OptionSetDescription("[INFO] Checking exif on photos..."),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stdout, "\n")
		}),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}))

	var photos []Photo
	for photo := range ch {
		bar.Add(1)
		photos = append(photos, photo)
	}

	// handle error in goroutines (secondary wait)
	return photos, eg.Wait()
}

func modify(file, datetime string, dryrun bool) (Photo, error) {
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return Photo{}, err
	}
	tc := &now.Config{
		WeekStartDay: time.Monday,
		TimeLocation: location,
		TimeFormats:  []string{"2006-01-02 15:04:05", "2006-01-02"},
	}
	ts := tc.MustParse(datetime)

	et, err := exiftool.NewExiftool()
	if err != nil {
		return Photo{}, fmt.Errorf("failed to run exiftool: %w", err)
	}
	defer et.Close()

	originals := et.ExtractMetadata(file)
	if len(originals) == 0 {
		return Photo{}, errors.New("failed to extract metadata")
	}
	withTimezone := "2006:01:02 15:04:05+09:00"
	withoutTimezone := "2006:01:02 15:04:05"
	withSubSeconds := "2006:01:02 15:04:05.00"
	thingsToModify := map[string]string{
		"DateTimeOriginal":       withoutTimezone,
		"ModifyDate":             withoutTimezone,
		"CreateDate":             withoutTimezone,
		"SubSecCreateDate":       withSubSeconds,
		"SubSecModifyDate":       withSubSeconds,
		"SubSecDateTimeOriginal": withSubSeconds,
		"FileModifyDate":         withTimezone,
		"FileInodeChangeDate":    withTimezone,
		"FileAccessDate":         withTimezone,
	}
	for thing, layout := range thingsToModify {
		// t, err := originals[0].GetString(thing)
		// if err != nil {
		// 	return Photo{}, err
		// }
		// tm, err := time.Parse(layout, t)
		// if err != nil {
		// 	return Photo{}, err
		// }
		// originals[0].SetString(thing, tm.AddDate(years, months, days).Format(layout))
		originals[0].SetString(thing, ts.Format(layout))
	}
	if dryrun {
		log.Printf("[DEBUG] DRY-RUN: Set %s (%s)", file, ts.Format("2006/01/02"))
	} else {
		log.Printf("[DEBUG] Set %s (%s)", file, ts.Format("2006/01/02"))
		et.WriteMetadata(originals)
	}

	return Photo{
		Name:     file,
		Metadata: originals[0],
	}, nil
}
