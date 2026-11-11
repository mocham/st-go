package main

import (
	"context"
	"errors"
	"time"
)

func (application *app) deleteSession(id string) {
	if id == "" || application.deleting[id] || application.deleted[id] {
		return
	}
	application.deleting[id] = true
	application.status, application.dirty = "Deleting session…", true
	go func() {
		ctx, cancel := context.WithTimeout(application.ctx, 60*time.Second)
		defer cancel()
		err := application.api.deleteSession(ctx, id)
		select {
		case application.operations <- operationResult{kind: "delete", requestID: id, err: err}:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) applyDelete(result operationResult) {
	delete(application.deleting, result.requestID)
	if application.deleted[result.requestID] {
		return
	}
	var failure *apiError
	if result.err != nil && !(errors.As(result.err, &failure) && failure.status == 404) {
		application.status, application.dirty = result.err.Error(), true
		return
	}
	application.removeSession(result.requestID)
	application.status = "Session deleted"
}

func (application *app) removeSession(id string) {
	application.deleted[id] = true
	// The server removes descendants too; remove known children immediately.
	for changed := true; changed; {
		changed = false
		for _, session := range application.sessions {
			if application.deleted[session.ParentID] && !application.deleted[session.ID] {
				application.deleted[session.ID], changed = true, true
			}
		}
	}
	remaining := application.sessions[:0]
	for _, session := range application.sessions {
		if !application.deleted[session.ID] {
			remaining = append(remaining, session)
		}
	}
	application.sessions = remaining
	application.sidebarHits, application.deleteHits = nil, nil
	application.catalogSequence++ // An in-flight page must not resurrect a deletion.
	application.catalogLoading = false
	application.catalogRefresh = time.Now()
	if application.deleted[application.attachTarget] {
		application.cancelAttach()
	}
	if application.deleted[application.session.ID] {
		application.resetFiles()
		directory := application.session.Location
		application.closePicker()
		application.cancelAttach()
		application.sessionEpoch++
		application.session = sessionInfo{Location: directory}
		application.messages, application.permissions = nil, nil
		application.rebuildIndex()
		application.selectedPrompt, application.activePrompt = "", ""
		application.busy, application.syncing, application.permissionBusy = false, false, false
		application.details, application.usage = sessionDetails{}, nil
		application.detailsLoading = false
		application.historyCursor, application.previewGeneration = "", ""
		application.queuePreview()
		if len(remaining) > 0 {
			application.attachSession(remaining[0].ID)
		}
	}
	application.dirty = true
}

func (application *app) newSession() {
	if application.attachTarget != "" {
		return
	}
	directory, id, epoch := application.session.Location.Directory, application.session.ID, application.sessionEpoch
	application.status = "Creating session…"
	application.dirty = true
	// Reuse attachment cancellation/last-selection protection.
	application.cancelAttach()
	application.attachTarget = "new"
	sequence := application.attachSequence
	ctx, cancel := context.WithTimeout(application.ctx, 60*time.Second)
	application.attachCancel = cancel
	go func() {
		defer cancel()
		created, err := application.api.createSession(ctx, directory)
		var snapshot sessionSnapshot
		if err == nil {
			snapshot, err = application.api.snapshot(ctx, created.ID, 10)
		}
		result := operationResult{kind: "attach", sessionID: id, epoch: epoch, attachToken: sequence, sessionSnapshot: snapshot, err: err}
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}
