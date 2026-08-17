package rclone

import "testing"

const sharedConf = `[gdrive]
type = drive
scope = drive
team_drive = 0AABBCC
token = {"access_token":"a","refresh_token":"r"}
`

func TestInspectRemote_NoClientIDIsShared(t *testing.T) {
	if got := InspectRemote(sharedConf, "gdrive"); got != SharedClient {
		t.Fatalf("want shared, got %s", got)
	}
}

func TestInspectRemote_BothHalvesIsOwn(t *testing.T) {
	conf := sharedConf + "client_id = 123.apps.googleusercontent.com\nclient_secret = s3cr3t\n"
	if got := InspectRemote(conf, "gdrive"); got != OwnClient {
		t.Fatalf("want own, got %s", got)
	}
}

func TestInspectRemote_HalfCredentialIsPartial(t *testing.T) {
	for name, extra := range map[string]string{
		"id only":     "client_id = 123.apps.googleusercontent.com\n",
		"secret only": "client_secret = s3cr3t\n",
	} {
		if got := InspectRemote(sharedConf+extra, "gdrive"); got != PartialClient {
			t.Fatalf("%s: want partial, got %s", name, got)
		}
	}
}

// An operator clearing a value rather than deleting the line leaves rclone on
// the shared client, so an empty assignment must not read as configured.
func TestInspectRemote_EmptyValueIsNotSet(t *testing.T) {
	conf := sharedConf + "client_id =\nclient_secret =   \n"
	if got := InspectRemote(conf, "gdrive"); got != SharedClient {
		t.Fatalf("want shared, got %s", got)
	}
}

func TestInspectRemote_IgnoresOtherRemotesSections(t *testing.T) {
	conf := "[other]\ntype = drive\nclient_id = x\nclient_secret = y\n\n" + sharedConf
	if got := InspectRemote(conf, "gdrive"); got != SharedClient {
		t.Fatalf("want shared for gdrive, got %s", got)
	}
	if got := InspectRemote(conf, "other"); got != OwnClient {
		t.Fatalf("want own for other, got %s", got)
	}
}

func TestInspectRemote_TrailingSectionBounded(t *testing.T) {
	conf := sharedConf + "\n[later]\nclient_id = x\nclient_secret = y\n"
	if got := InspectRemote(conf, "gdrive"); got != SharedClient {
		t.Fatalf("want shared, got %s", got)
	}
}

func TestInspectRemote_UnknownRemoteIsShared(t *testing.T) {
	if got := InspectRemote(sharedConf, "nope"); got != SharedClient {
		t.Fatalf("want shared, got %s", got)
	}
}
