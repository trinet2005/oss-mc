// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"io"
	"os"
	"runtime/debug"
	"syscall"

	"github.com/dustin/go-humanize"
	"github.com/minio/cli"
	minio "github.com/trinet2005/oss-go-sdk"
	"github.com/trinet2005/oss-mc/pkg/probe"
)

func defaultPartSize() string {
	_, partSize, _, _ := minio.OptimalPartInfo(-1, 0)
	return humanize.IBytes(uint64(partSize))
}

var pipeFlags = []cli.Flag{
	cli.StringFlag{
		Name:  "encrypt",
		Usage: "encrypt objects (using server-side encryption with server managed keys)",
	},
	cli.StringFlag{
		Name:  "storage-class, sc",
		Usage: "set storage class for new object(s) on target",
	},
	cli.StringFlag{
		Name:  "attr",
		Usage: "add custom metadata for the object",
	},
	cli.StringFlag{
		Name:  "tags",
		Usage: "apply one or more tags to the uploaded objects",
	},
	cli.IntFlag{
		Name:  "concurrent",
		Value: 1,
		Usage: "allow N concurrent uploads [WARNING: will use more memory use it with caution]",
	},
	cli.StringFlag{
		Name:  "part-size",
		Value: defaultPartSize(),
		Usage: "customize chunk size for each concurrent upload",
	},
	cli.IntFlag{
		Name:   "pipe-max-size",
		Usage:  "increase the pipe buffer size to a custom value",
		Hidden: true,
	},
}

// Display contents of a file.
var pipeCmd = cli.Command{
	Name:         "pipe",
	Usage:        "stream STDIN to an object",
	Action:       mainPipe,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        append(append(pipeFlags, ioFlags...), globalFlags...),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] [TARGET]
{{if .VisibleFlags}}
FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}{{end}}
ENVIRONMENT VARIABLES:
  MC_ENCRYPT:      list of comma delimited prefix values
  MC_ENCRYPT_KEY:  list of comma delimited prefix=secret values

EXAMPLES:
  1. Write contents of stdin to a file on local filesystem.
     {{.Prompt}} {{.HelpName}} /tmp/hello-world.go

  2. Write contents of stdin to an object on Amazon S3 cloud storage.
     {{.Prompt}} {{.HelpName}} s3/personalbuck/meeting-notes.txt

  3. Copy an ISO image to an object on Amazon S3 cloud storage.
     {{.Prompt}} cat debian-8.2.iso | {{.HelpName}} s3/opensource-isos/gnuos.iso

  4. Stream MySQL database dump to Amazon S3 directly.
     {{.Prompt}} mysqldump -u root -p ******* accountsdb | {{.HelpName}} s3/sql-backups/backups/accountsdb-oct-9-2015.sql

  5. Write contents of stdin to an object on Amazon S3 cloud storage and assign REDUCED_REDUNDANCY storage-class to the uploaded object.
     {{.Prompt}} {{.HelpName}} --storage-class REDUCED_REDUNDANCY s3/personalbuck/meeting-notes.txt

  6. Copy to MinIO cloud storage with specified metadata, separated by ";"
      {{.Prompt}} cat music.mp3 | {{.HelpName}} --attr "Cache-Control=max-age=90000,min-fresh=9000;Artist=Unknown" play/mybucket/music.mp3

  7. Set tags to the uploaded objects
      {{.Prompt}} tar cvf - . | {{.HelpName}} --tags "category=prod&type=backup" play/mybucket/backup.tar
`,
}

func pipe(ctx *cli.Context, targetURL string, encKeyDB map[string][]prefixSSEPair, meta map[string]string) *probe.Error {
	// If possible increase the pipe buffer size
	if e := increasePipeBufferSize(os.Stdin, ctx.Int("pipe-max-size")); e != nil {
		fatalIf(probe.NewError(e), "Unable to increase custom pipe-max-size")
	}

	if targetURL == "" {
		// When no target is specified, pipe cat's stdin to stdout.
		return catOut(os.Stdin, -1).Trace()
	}

	storageClass := ctx.String("storage-class")
	alias, _ := url2Alias(targetURL)
	sseKey := getSSE(targetURL, encKeyDB[alias])

	multipartThreads := ctx.Int("concurrent")
	if multipartThreads > 1 {
		// We will be allocating large buffers, reduce default GC overhead
		debug.SetGCPercent(20)
	}

	var multipartSize uint64
	var e error
	if partSizeStr := ctx.String("part-size"); partSizeStr != "" {
		multipartSize, e = humanize.ParseBytes(partSizeStr)
		if e != nil {
			return probe.NewError(e)
		}
	}

	// Stream from stdin to multiple objects until EOF.
	// Ignore size, since os.Stat() would not return proper size all the time
	// for local filesystem for example /proc files.
	opts := PutOptions{
		sse:              sseKey,
		storageClass:     storageClass,
		metadata:         meta,
		multipartSize:    multipartSize,
		multipartThreads: uint(multipartThreads),
		concurrentStream: ctx.IsSet("concurrent"),
	}

	pg := newProgressBar(0)

	_, err := putTargetStreamWithURL(targetURL, io.TeeReader(os.Stdin, pg), -1, opts)
	// TODO: See if this check is necessary.
	switch e := err.ToGoError().(type) {
	case *os.PathError:
		if e.Err == syscall.EPIPE {
			// stdin closed by the user. Gracefully exit.
			return nil
		}
	}
	return err.Trace(targetURL)
}

// checkPipeSyntax - validate arguments passed by user
func checkPipeSyntax(ctx *cli.Context) {
	if len(ctx.Args()) != 1 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code.
	}
}

// mainPipe is the main entry point for pipe command.
func mainPipe(ctx *cli.Context) error {
	// validate pipe input arguments.
	checkPipeSyntax(ctx)
	// Parse encryption keys per command.
	encKeyDB, err := getEncKeys(ctx)
	fatalIf(err, "Unable to parse encryption keys.")

	// validate pipe input arguments.
	checkPipeSyntax(ctx)

	meta := map[string]string{}
	if attr := ctx.String("attr"); attr != "" {
		meta, err = getMetaDataEntry(attr)
		fatalIf(err.Trace(attr), "Unable to parse --attr value")
	}
	if tags := ctx.String("tags"); tags != "" {
		meta["X-Amz-Tagging"] = tags
	}
	if len(ctx.Args()) == 0 {
		err = pipe(ctx, "", nil, meta)
		fatalIf(err.Trace("stdout"), "Unable to write to one or more targets.")
	} else {
		// extract URLs.
		URLs := ctx.Args()
		err = pipe(ctx, URLs[0], encKeyDB, meta)
		fatalIf(err.Trace(URLs[0]), "Unable to write to one or more targets.")
	}

	// Done.
	return nil
}
