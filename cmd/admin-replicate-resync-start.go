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
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/trinet2005/oss-admin-go"
	"github.com/trinet2005/oss-mc/pkg/probe"
	"github.com/trinet2005/oss-pkg/console"
)

var adminReplicateResyncStartCmd = cli.Command{
	Name:         "start",
	Usage:        "start resync to site",
	Action:       mainAdminReplicateResyncStart,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        globalFlags,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} ALIAS1 ALIAS2

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}

EXAMPLES:
  1. Resync bucket data from minio1 to minio2
     {{.Prompt}} {{.HelpName}} minio1 minio2
`,
}

type resyncMessage madmin.SRResyncOpStatus

func (m resyncMessage) JSON() string {
	bs, e := json.MarshalIndent(madmin.SRResyncOpStatus(m), "", " ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")
	return string(bs)
}

func (m resyncMessage) String() string {
	v := madmin.SRResyncOpStatus(m)
	messages := []string{}
	th := "ResyncMessage"
	if v.ErrDetail != "" {
		messages = append(messages, v.ErrDetail)
		th = "ResyncErr"
	} else {
		messages = append(messages, fmt.Sprintf("Site resync started with ID %s", v.ResyncID))
	}
	return console.Colorize(th, strings.Join(messages, "\n"))
}

func mainAdminReplicateResyncStart(ctx *cli.Context) error {
	{
		// Check argument count
		argsNr := len(ctx.Args())
		if argsNr != 2 {
			cli.ShowCommandHelpAndExit(ctx, "start", 1) // last argument is exit code
		}
	}

	console.SetColor("ResyncMessage", color.New(color.FgGreen))
	console.SetColor("ResyncErr", color.New(color.FgRed))

	// Get the alias parameter from cli
	args := ctx.Args()
	aliasedURL := args.Get(0)
	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	fatalIf(err, "Unable to initialize admin connection.")
	info, e := client.SiteReplicationInfo(globalContext)
	fatalIf(probe.NewError(e), "Unable to fetch site replication info.")
	var peer madmin.PeerInfo
	for _, site := range info.Sites {
		if args[1] == site.Name {
			peer = site
		}
	}
	if peer.DeploymentID == "" {
		fatalIf(errInvalidArgument().Trace(ctx.Args().Tail()...),
			"alias provided is not part of cluster replication.")
	}
	res, e := client.SiteReplicationResyncOp(globalContext, peer, madmin.SiteResyncStart)
	fatalIf(probe.NewError(e).Trace(args...), "Unable to start replication resync")
	printMsg(resyncMessage(res))

	return nil
}
