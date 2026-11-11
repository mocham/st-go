package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

type sessionDetails struct {
	models       []modelInfo
	defaultModel *modelInfo
	lastModel    *modelRef
}

type contextUsage struct {
	tokens tokenUsage
	model  modelRef
}

func (application *app) requestDetails() {
	if application.detailsLoading || application.api == nil || application.session.ID == "" {
		return
	}
	application.detailsLoading = true
	application.detailsRefresh = time.Now().Add(time.Minute)
	session, epoch := application.session, application.sessionEpoch
	go func() {
		ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
		defer cancel()
		result := operationResult{kind: "details", sessionID: session.ID, epoch: epoch, details: &sessionDetails{}}
		result.details.models, result.err = application.api.models(ctx, session.Location.Directory)
		if result.err == nil && session.Model == nil {
			result.details.defaultModel, result.err = application.api.defaultModel(ctx, session.Location.Directory)
		}
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) effectiveModel() modelRef {
	if application.session.Model != nil {
		return *application.session.Model
	}
	if application.details.lastModel != nil {
		return *application.details.lastModel
	}
	if model := application.details.defaultModel; model != nil {
		return modelRef{ID: model.ID, ProviderID: model.ProviderID}
	}
	return modelRef{}
}

func (application *app) updateUsage(messages []apiMessage) {
	application.usage = nil
	boundary := ""
	if application.session.Revert != nil {
		boundary = application.session.Revert.MessageID
	}
	if boundary != "" {
		found := false
		for _, message := range messages {
			if message.ID == boundary {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	for _, message := range messages {
		if message.ID == boundary {
			break
		}
		if message.Type == "compaction" && message.Status == "completed" {
			application.usage = nil
		}
		if message.Type != "assistant" {
			continue
		}
		if message.Model != nil {
			application.details.lastModel = message.Model
		}
		if message.Tokens != nil {
			model := application.effectiveModel()
			if message.Model != nil {
				model = *message.Model
			}
			application.usage = &contextUsage{tokens: *message.Tokens, model: model}
		}
	}
}

func (application *app) contextPercent() string {
	model := application.effectiveModel()
	tokens := 0.0
	if application.usage != nil {
		model, tokens = application.usage.model, application.usage.tokens.total()
	}
	models := application.details.models
	if application.details.defaultModel != nil {
		models = append(append([]modelInfo(nil), models...), *application.details.defaultModel)
	}
	for _, info := range models {
		if info.ID == model.ID && info.ProviderID == model.ProviderID && info.Limit.Context > 0 {
			return fmt.Sprintf("%.0f%%", math.Round(tokens/float64(info.Limit.Context)*100))
		}
	}
	return "—"
}

func (application *app) modelLabel() string {
	model := application.effectiveModel()
	if model.ID == "" {
		return "resolving…"
	}
	if model.ProviderID == "" {
		return model.ID
	}
	return model.ProviderID + "/" + model.ID
}

func (application *app) variantLabel() string {
	variant := application.effectiveModel().Variant
	if variant == "" {
		return "default"
	}
	return variant
}

func (application *app) headerHeight() int {
	if application.rows >= 7 {
		return 2
	}
	return 1
}

func (application *app) detailsLine() string {
	context := "Context: " + application.contextPercent()
	prefix, separator := " Variant: ", " · "
	space := application.cols - textWidth(prefix+separator+context)
	if space < 1 {
		return application.compactDetails()
	}
	return prefix + truncateCells(application.variantLabel(), space) + separator + context
}

func (application *app) compactDetails() string {
	context := application.contextPercent()
	space := max(0, application.cols-textWidth(context)-2)
	variant := truncateCells(application.variantLabel(), space/3)
	model := truncateCells(application.effectiveModel().ID, max(0, space-textWidth(variant)))
	return strings.TrimSpace(model + "#" + variant + " " + context)
}
