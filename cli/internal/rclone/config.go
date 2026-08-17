// Package rclone reads the parts of an rclone config file the platform needs
// to reason about, without depending on rclone itself.
package rclone

import "regexp"

// OAuthClient describes which OAuth application a remote authenticates as.
type OAuthClient int

const (
	// SharedClient is a remote with no client_id, which makes rclone fall back
	// to the credentials bundled in its binary. Those are shared by every
	// rclone user, heavily rate limited, and Google is retiring them.
	SharedClient OAuthClient = iota
	// OwnClient is a remote carrying both halves of a dedicated credential.
	OwnClient
	// PartialClient is one half of a credential without the other. rclone
	// sends both on every token refresh, so half a credential authenticates as
	// nothing and the remote stops working at the first refresh.
	PartialClient
)

func (c OAuthClient) String() string {
	switch c {
	case OwnClient:
		return "own"
	case PartialClient:
		return "partial"
	default:
		return "shared"
	}
}

// Horizontal whitespace only: \s would span the line break and let a key with
// an empty value swallow the next line as its own.
var (
	sectionRE = regexp.MustCompile(`(?m)^[ \t]*\[([^\]]+)\][ \t\r]*$`)
	clientRE  = regexp.MustCompile(`(?m)^[ \t]*(client_id|client_secret)[ \t]*=[ \t]*(.*?)[ \t\r]*$`)
)

// InspectRemote reports which OAuth client the named remote's section of conf
// authenticates as. A remote that is not present in conf reads as SharedClient:
// absent config and absent client_id put rclone on the same code path.
func InspectRemote(conf, remote string) OAuthClient {
	var id, secret bool
	for _, m := range clientRE.FindAllStringSubmatch(remoteSection(conf, remote), -1) {
		if m[2] == "" {
			continue
		}
		if m[1] == "client_id" {
			id = true
		} else {
			secret = true
		}
	}
	switch {
	case id && secret:
		return OwnClient
	case id || secret:
		return PartialClient
	default:
		return SharedClient
	}
}

// remoteSection returns the body of conf's [remote] section: everything after
// its header up to the next header or end of file. Scoping matters because a
// config may hold several remotes and only one of them is being asked about.
func remoteSection(conf, remote string) string {
	for _, loc := range sectionRE.FindAllStringSubmatchIndex(conf, -1) {
		if conf[loc[2]:loc[3]] != remote {
			continue
		}
		body := conf[loc[1]:]
		if next := sectionRE.FindStringIndex(body); next != nil {
			return body[:next[0]]
		}
		return body
	}
	return ""
}
