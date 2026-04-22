package modmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ModItemType defines the type of a mod item (file or directory).
type ModItemType string

const (
	TypeFile ModItemType = "file"
	TypeDir  ModItemType = "directory"
	TypeURL  ModItemType = "url"
	TypeMD   ModItemType = "markdown"
)

// ModItem represents a file or directory in the mod structure.
type ModItem struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"` // Relative path from the base mod directory
	Type     ModItemType `json:"type"`
	Size     int64       `json:"size,omitempty"`     // For files
	URL      string      `json:"url,omitempty"`      // For .url files
	Markdown string      `json:"markdown,omitempty"` // For .md files content
	Children []ModItem   `json:"children,omitempty"` // For directories
}

// ScanModDirectory scans the mod directory for a given server and returns its hierarchical structure.
func ScanModDirectory(baseDataPath, serverName string) (*ModItem, error) {
	// Security check: prevent path traversal
	if strings.Contains(serverName, "..") || strings.Contains(serverName, "/") || strings.Contains(serverName, "\\") {
		return nil, fmt.Errorf("invalid server name: %s", serverName)
	}

	basePath := filepath.Join(baseDataPath, serverName)

	// Check if the base directory exists
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("mod directory for server %s not found: %w", serverName, err)
	}

	root := &ModItem{
		Name: serverName,
		Path: "", // Root has empty path
		Type: TypeDir,
	}

	err := walkDir(basePath, "", root)
	if err != nil {
		return nil, fmt.Errorf("error walking mod directory for server %s: %w", serverName, err)
	}

	return root, nil
}

// walkDir recursively walks the directory and builds the ModItem tree.
func walkDir(currentPath string, relativePath string, parent *ModItem) error {
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		itemPath := filepath.Join(relativePath, entry.Name())
		item := ModItem{
			Name: entry.Name(),
			Path: itemPath,
		}

		if entry.IsDir() {
			item.Type = TypeDir
			if err := walkDir(filepath.Join(currentPath, entry.Name()), itemPath, &item); err != nil {
				return err
			}
		} else {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			item.Size = info.Size()

			switch {
			case strings.HasSuffix(entry.Name(), ".url"):
				item.Type = TypeURL
				content, err := os.ReadFile(filepath.Join(currentPath, entry.Name()))
				if err != nil {
					return fmt.Errorf("failed to read .url file %s: %w", itemPath, err)
				}
				item.URL = strings.TrimSpace(string(content))
			case strings.HasSuffix(entry.Name(), ".md"):
				item.Type = TypeMD
				content, err := os.ReadFile(filepath.Join(currentPath, entry.Name()))
				if err != nil {
					return fmt.Errorf("failed to read .md file %s: %w", itemPath, err)
				}
				item.Markdown = string(content)
			default:
				item.Type = TypeFile
			}
		}
		parent.Children = append(parent.Children, item)
	}
	return nil
}
