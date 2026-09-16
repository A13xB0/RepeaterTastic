package web

import (
	"context"
	"fmt"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// pushOwner writes a node identity's names to the node.
func pushOwner(ctx context.Context, id *mesh.Identity) error {
	rm := id.Remote()
	if rm == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: id.UserCopy()}}); err != nil {
		return fmt.Errorf("saved here, but meshtasticd didn't take the new name: %w", err)
	}
	return nil
}

// pushChannels writes a node identity's channel slots to the node.
func pushChannels(ctx context.Context, id *mesh.Identity, slots ...int) error {
	rm := id.Remote()
	if rm == nil || len(slots) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, i := range slots {
		if _, err := rm.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: id.ChannelCopy(i)}}); err != nil {
			return fmt.Errorf("saved here, but meshtasticd didn't take channel %d: %w", i, err)
		}
	}
	return nil
}
