package phoneapi

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

// appAdminAllowed reports whether an app may send an admin message to its own node while the
// identity doesn't let apps change node settings. Reading anything is fine, and so are the things
// RepeaterTastic keeps in step from the node: its names, channels and node list. Radio, device,
// module and position settings, reboots and resets are RepeaterTastic's to manage.
func appAdminAllowed(m *pb.AdminMessage) bool {
	switch m.PayloadVariant.(type) {
	case *pb.AdminMessage_SetOwner, *pb.AdminMessage_SetChannel,
		*pb.AdminMessage_BeginEditSettings, *pb.AdminMessage_CommitEditSettings,
		*pb.AdminMessage_SetFavoriteNode, *pb.AdminMessage_RemoveFavoriteNode,
		*pb.AdminMessage_SetIgnoredNode, *pb.AdminMessage_RemoveIgnoredNode,
		*pb.AdminMessage_ToggleMutedNode, *pb.AdminMessage_RemoveByNodenum,
		*pb.AdminMessage_AddContact, *pb.AdminMessage_KeyVerification,
		*pb.AdminMessage_SetTimeOnly, *pb.AdminMessage_StoreUiConfig:
		return true
	}
	return strings.HasPrefix(adminKind(m), "Get")
}

// adminKind names an admin message's request, e.g. "SetConfig".
func adminKind(m *pb.AdminMessage) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", m.GetPayloadVariant()), "*pb.AdminMessage_")
}

// refusedAdmin reports whether p is an admin message to the app's own node that the identity's
// settings don't allow, and says what it was.
func (s *Session) refusedAdmin(p *pb.MeshPacket) (string, bool) {
	d := p.GetDecoded()
	if p.To != s.id.NodeNum || d.GetPortnum() != pb.PortNum_ADMIN_APP || s.id.Settings().AppSettings {
		return "", false
	}
	var m pb.AdminMessage
	if proto.Unmarshal(d.GetPayload(), &m) != nil {
		return "", false // the node refuses what it can't read
	}
	if appAdminAllowed(&m) {
		return "", false
	}
	return adminKind(&m), true
}
