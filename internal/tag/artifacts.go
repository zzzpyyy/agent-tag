package tag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxSharedArtifacts    = 4
	maxSharedArtifactSize = 512 << 10
)

var artifactNamePattern = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func safeArtifactName(value string) string {
	value = artifactNamePattern.ReplaceAllString(strings.TrimSpace(value), "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "agent"
	}
	return value
}

func (s *WebServer) persistRunArtifacts(ownerID, conversationID, agentName string, artifacts []RunArtifact) ([]ChatArtifact, error) {
	if len(artifacts) > maxSharedArtifacts {
		artifacts = artifacts[:maxSharedArtifacts]
	}
	targetDirectory := filepath.Join(TagDir(s.store.Root), "artifacts", ownerID, conversationID)
	if err := os.MkdirAll(targetDirectory, 0o700); err != nil {
		return nil, err
	}
	result := []ChatArtifact{}
	for _, artifact := range artifacts {
		sourcePath, err := filepath.Abs(artifact.Path)
		if err != nil {
			continue
		}
		info, err := os.Stat(sourcePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSharedArtifactSize {
			continue
		}
		token, err := randomToken(5)
		if err != nil {
			return result, err
		}
		extension := filepath.Ext(sourcePath)
		if len(extension) > 10 || strings.ContainsAny(extension, `/\\`) {
			extension = ".txt"
		}
		if extension == "" {
			extension = ".txt"
		}
		targetPath := filepath.Join(targetDirectory, safeArtifactName(agentName)+"-"+strings.ToLower(token)+extension)
		source, err := os.Open(sourcePath)
		if err != nil {
			continue
		}
		target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = io.Copy(target, io.LimitReader(source, maxSharedArtifactSize+1))
		}
		sourceCloseErr := source.Close()
		if target != nil {
			if targetCloseErr := target.Close(); err == nil {
				err = targetCloseErr
			}
		}
		if err == nil {
			err = sourceCloseErr
		}
		if err != nil {
			_ = os.Remove(targetPath)
			return result, fmt.Errorf("共享 Agent 产物失败: %w", err)
		}
		label := strings.TrimSpace(artifact.Label)
		if label == "" {
			label = "Agent 工具输出"
		}
		contents, err := os.ReadFile(targetPath)
		if err != nil {
			return result, err
		}
		digest := sha256.Sum256(contents)
		mediaType := mime.TypeByExtension(extension)
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		artifactID := "artifact-" + strings.ToLower(token)
		record := ArtifactRecord{ID: artifactID, OwnerID: ownerID, ConversationID: conversationID, Agent: agentName, Path: targetPath, Label: label, MediaType: mediaType, Size: int64(len(contents)), SHA256: hex.EncodeToString(digest[:]), CreatedAt: Now()}
		if err := s.store.SaveArtifact(record); err != nil {
			_ = os.Remove(targetPath)
			return result, err
		}
		result = append(result, ChatArtifact{ID: artifactID, Label: label, MediaType: mediaType, Size: record.Size, SHA256: record.SHA256})
	}
	return result, nil
}
