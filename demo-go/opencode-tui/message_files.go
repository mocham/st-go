package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

type messageFile struct {
	ID, Name, Path, URI, MIME, Data string
	Inline                          bool
	Text                            *string // saved textual tool output, previewed as a message-owned file
}

type fileSelection struct {
	ref     messageFile
	text    string
	media   *fileMedia
	loading bool
}

type fileResult struct {
	sequence uint64
	text     string
	media    *fileMedia
}

type fileMedia struct {
	data      []byte
	extension string
}

// Files come from structured arguments/results and explicit message links,
// never from shell tokenization or guesses about arbitrary prose.
func toolFiles(tool messageContent) []messageFile {
	var files []messageFile
	seen := make(map[string]bool)
	add := func(value string) {
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		files = append(files, messageFile{Name: value, Path: value})
	}
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				item := value[key]
				switch key {
				case "file", "filePath", "file_path", "filename", "outputPath", "outputFile", "movePath", "relativePath":
					if text, ok := item.(string); ok {
						add(text)
					}
				case "path":
					name := tool.Name[strings.LastIndex(tool.Name, ".")+1:]
					if name == "read" || name == "write" || name == "edit" || name == "patch" {
						if text, ok := item.(string); ok {
							add(text)
						}
					}
				case "patchText", "patch", "patch_text":
					if text, ok := item.(string); ok {
						for _, line := range strings.Split(text, "\n") {
							for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
								if name, ok := strings.CutPrefix(line, prefix); ok {
									add(strings.TrimSpace(name))
								}
							}
						}
					}
				case "files":
					if items, ok := item.([]any); ok {
						for _, file := range items {
							if text, ok := file.(string); ok {
								add(text)
							} else {
								collect(file)
							}
						}
					}
				default:
					// Nested calls (e.g. parallel tools) retain their explicit file fields.
					if _, ok := item.(map[string]any); ok {
						collect(item)
					}
				}
			}
		}
	}
	collect(toolInput(tool))
	collect(tool.State.Metadata)
	var output []string
	for _, content := range tool.State.Content {
		if content.Type == "file" {
			name := content.Name
			if name == "" {
				name = uriName(content.URI)
			}
			files = append(files, messageFile{Name: name, URI: content.URI, MIME: content.MIME})
		} else if content.Type == "text" && content.Text != "" {
			output = append(output, content.Text)
		}
	}
	if len(output) > 0 {
		text := strings.Join(output, "\n\n")
		name := tool.Name[strings.LastIndex(tool.Name, ".")+1:]
		if name == "" {
			name = "tool"
		}
		files = append(files, messageFile{ID: "output:" + tool.ID, Name: name + "-" + shortID(tool.ID) + ".txt", Text: &text})
	}
	return files
}

func uriName(uri string) string {
	parsed, err := url.Parse(uri)
	if err == nil && parsed.Scheme != "data" && parsed.Path != "" {
		return path.Base(parsed.Path)
	}
	return "attachment"
}

var messageFileLink = regexp.MustCompile(`!?\[[^\]\n]*\]\((<[^>\n]+>|[^\s)]+)(?:\s+"[^"\n]*")?\)`)

func linkedFiles(text string) []messageFile {
	var files []messageFile
	for _, match := range messageFileLink.FindAllStringSubmatch(text, -1) {
		name := strings.Trim(match[1], "<>")
		u, err := url.Parse(name)
		if err != nil || u.Host != "" && (u.Scheme != "file" || u.Host != "localhost") || u.Scheme != "" && u.Scheme != "file" || u.Path == "" {
			continue
		}
		files = append(files, messageFile{Name: u.Path, Path: u.Path})
	}
	return files
}

func (application *app) messageFiles() []messageFile {
	var files []messageFile
	seen := make(map[string]bool)
	add := func(file messageFile) {
		if file.URI != "" {
			if u, err := url.Parse(file.URI); err == nil && u.Scheme == "file" && (u.Host == "" || u.Host == "localhost") {
				file.Path, file.URI = u.Path, ""
			}
		}
		if file.Path != "" {
			if !path.IsAbs(file.Path) && path.IsAbs(application.session.Location.Directory) {
				file.Path = path.Join(application.session.Location.Directory, file.Path)
			}
			file.Path = path.Clean(file.Path)
			file.ID = "path:" + file.Path
		} else if file.ID == "" && file.URI != "" {
			file.ID = fmt.Sprintf("uri:%x", sha256.Sum256([]byte(file.URI)))
		}
		if file.ID == "" || seen[file.ID] {
			return
		}
		seen[file.ID] = true
		files = append(files, file)
	}
	for _, message := range application.messages {
		if message.ID != application.selectedPrompt && message.PromptID != application.selectedPrompt {
			continue
		}
		for index, attachment := range message.Files {
			name := attachment.Name
			if name == "" {
				name = uriName(attachment.Source.URI)
			}
			file := messageFile{ID: fmt.Sprintf("%s:attachment:%d", message.ID, index), Name: name, MIME: attachment.MIME, Data: attachment.Data, Inline: true}
			add(file)
		}
		for _, tool := range message.Tools {
			for _, file := range toolFiles(tool) {
				if file.Text != nil {
					file.ID = message.ID + ":" + file.ID
				}
				add(file)
			}
		}
		for _, name := range message.ChangedFiles {
			add(messageFile{Name: name, Path: name})
		}
		text := message.Rich
		if message.Role == "user" {
			text = message.Text
		}
		for _, file := range linkedFiles(text) {
			add(file)
		}
	}
	return files
}

func (application *app) fileRows(width int) []displayRow {
	files := application.messageFiles()
	if len(files) == 0 {
		return []displayRow{{text: "No files in this prompt's messages.", style: dimStyle}, {text: "Tab returns to prompts.", style: dimStyle}}
	}
	var rows []displayRow
	for _, file := range files {
		style, marker := bodyStyle, "  "
		if application.fileSelection != nil && application.fileSelection.ref.ID == file.ID {
			style, marker = selectedPromptStyle, "› "
		}
		for index, line := range wrapText(file.Name, max(1, width-2)) {
			prefix := "  "
			if index == 0 {
				prefix = marker
			}
			rows = append(rows, displayRow{text: prefix + line, style: style, fileID: file.ID})
		}
	}
	return rows
}

func (application *app) cancelFile() {
	if application.fileCancel != nil {
		application.fileCancel()
		application.fileCancel = nil
	}
	application.fileSequence++
}

func (application *app) resetFiles() {
	application.cancelFile()
	if application.filesMode {
		application.scroll = application.promptScroll
	}
	application.filesMode, application.fileSelection = false, nil
	application.filesScroll = 0
	application.sidebarHits = nil
}

func (application *app) toggleFiles() {
	application.cancelFile()
	if application.filesMode {
		application.filesScroll, application.scroll = application.scroll, application.promptScroll
		application.fileSelection = nil
	} else {
		application.promptScroll, application.scroll = application.scroll, application.filesScroll
		application.followPreview = false
	}
	application.filesMode = !application.filesMode
	application.sidebarFocus = ""
	application.sidebarHits, application.deleteHits = nil, nil
	application.queuePreview()
	application.dirty = true
}

func (application *app) selectFile(id string) {
	if !application.filesMode {
		return
	}
	for _, file := range application.messageFiles() {
		if file.ID == id {
			application.loadFile(file)
			return
		}
	}
}

func (application *app) loadFile(file messageFile) {
	application.cancelFile()
	application.fileSelection = &fileSelection{ref: file, loading: true, text: "Loading " + safeText(file.Name) + "…"}
	application.queuePreview()
	application.dirty = true
	id, epoch, sequence, directory := application.session.ID, application.sessionEpoch, application.fileSequence, application.session.Location.Directory
	ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
	application.fileCancel = cancel
	go func() {
		defer cancel()
		data, mime, err := application.api.readMessageFile(ctx, directory, file)
		result := operationResult{kind: "file", sessionID: id, epoch: epoch, err: err, file: &fileResult{sequence: sequence}}
		if err == nil {
			result.file.text, result.file.media = filePreview(file.Name, mime, data)
		}
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) applyFileResult(result operationResult) {
	if !application.filesMode || application.fileSelection == nil || result.file.sequence != application.fileSequence {
		return
	}
	selected := application.fileSelection
	selected.loading = false
	selected.text, selected.media = result.file.text, result.file.media
	if result.err != nil {
		selected.text = "Unable to preview " + safeText(selected.ref.Name) + ":\n\n" + safeText(result.err.Error())
	}
	application.queuePreview()
	application.dirty = true
}

func (application *app) reloadFile() {
	if application.fileSelection != nil {
		for _, file := range application.messageFiles() {
			if file.ID == application.fileSelection.ref.ID {
				application.loadFile(file)
				return
			}
		}
		application.loadFile(application.fileSelection.ref)
	}
}

func (application *app) moveFile(delta int) {
	files := application.messageFiles()
	if len(files) == 0 {
		return
	}
	index := -1
	for i, file := range files {
		if application.fileSelection != nil && file.ID == application.fileSelection.ref.ID {
			index = i
			break
		}
	}
	index = max(0, min(len(files)-1, index+delta))
	application.selectFile(files[index].ID)
	rows := application.fileRows(application.bodyLayout().prompts.width)
	for i, row := range rows {
		if row.fileID == files[index].ID {
			if i < application.scroll {
				application.scroll = i
			}
			if i >= application.scroll+application.bodyLayout().prompts.height {
				application.scroll = i - application.bodyLayout().prompts.height + 1
			}
			break
		}
	}
}
