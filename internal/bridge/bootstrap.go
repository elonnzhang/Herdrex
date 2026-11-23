package bridge

import (
	"context"

	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

type bootstrapRequest struct {
	creator     string
	dataRef     string
	subprotocol string
}

func bootstrapRequests(raw any) ([]bootstrapRequest, error) {
	if raw == nil {
		return nil, nil
	}
	items, valid := raw.([]any)
	if !valid || len(items) > 8 {
		return nil, rex.NewError(rex.CodeBadRequest, "data_tickets must be an array of at most 8 entries", 400)
	}
	requests := make([]bootstrapRequest, 0, len(items))
	for _, item := range items {
		object, valid := item.(map[string]any)
		if !valid {
			return nil, rex.NewError(rex.CodeBadRequest, "data_tickets entries must be objects", 400)
		}
		creator, _ := object["creator_name"].(string)
		dataRef, _ := object["data_ref_id"].(string)
		subprotocol, hasProtocol := object["subprotocol"]
		protocol, valid := subprotocol.(string)
		if creator == "" || dataRef == "" || hasProtocol && !valid {
			return nil, rex.NewError(rex.CodeBadRequest, "data_tickets entries need creator_name and data_ref_id strings, and an optional subprotocol string", 400)
		}
		requests = append(requests, bootstrapRequest{creator, dataRef, protocol})
	}
	return requests, nil
}

type bootstrapTicket struct {
	rexsvr.DataTicket
	SessionID   string `json:"session_id"`
	BlockID     string `json:"block_id"`
	DataRefID   string `json:"data_ref_id"`
	Subprotocol string `json:"subprotocol,omitempty"`
}

func (b *Bridge) bootstrapTickets(ctx context.Context, client *rexsvr.Client, out map[string]any, attached []map[string]any, requests []bootstrapRequest) {
	if len(requests) == 0 {
		return
	}
	var tickets []bootstrapTicket
	seen := map[rexsvr.DataRequest]bool{}
	for _, session := range attached {
		view := session["view"].(*rex.View)
		for _, window := range view.Windows {
			// Rex's schema: a ticket for each visible block, and a session
			// shows its active window.
			if view.ActiveWindowID == nil || window.WindowID != *view.ActiveWindowID {
				continue
			}
			for _, layer := range window.Layers {
				for _, block := range layer.Blocks {
					if window.ZoomedBlockID != nil && block.BlockID != *window.ZoomedBlockID {
						continue
					}
					for _, request := range requests {
						if block.CreatorName != request.creator {
							continue
						}
						data := rexsvr.DataRequest{SessionID: view.SessionID, BlockID: block.BlockID, DataRefID: request.dataRef, Subprotocol: request.subprotocol}
						if seen[data] {
							continue
						}
						seen[data] = true
						ticket, err := b.srv.IssueDataTicket(ctx, client, data)
						if err == nil {
							tickets = append(tickets, bootstrapTicket{ticket, data.SessionID, data.BlockID, data.DataRefID, data.Subprotocol})
						}
					}
				}
			}
		}
	}
	if len(tickets) > 0 {
		out["data_tickets"] = tickets
	}
}
