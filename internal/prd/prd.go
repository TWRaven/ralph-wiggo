package prd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// UserStory represents a single user story in the PRD.
type UserStory struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Priority           int      `json:"priority"`
	Passes             bool     `json:"passes"`
	Notes              string   `json:"notes"`
}

// PRD represents the full product requirements document.
type PRD struct {
	Project     string      `json:"project"`
	BranchName  string      `json:"branchName"`
	Description string      `json:"description"`
	UserStories []UserStory `json:"userStories"`
}

// LoadPRD reads and parses a prd.json file from the given path.
func LoadPRD(path string) (*PRD, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading PRD file: %w", err)
	}

	var prd PRD
	if err := json.Unmarshal(data, &prd); err != nil {
		return nil, fmt.Errorf("parsing PRD JSON: %w", err)
	}

	return &prd, nil
}

// SavePRD writes a PRD to the given path with indented JSON formatting.
func SavePRD(path string, prd *PRD) error {
	data, err := json.MarshalIndent(prd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling PRD: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing PRD file: %w", err)
	}

	return nil
}

// Validate checks a PRD for consistency: unique story IDs and sequential priorities.
func Validate(prd *PRD) error {
	if len(prd.UserStories) == 0 {
		return nil
	}

	// Check unique IDs.
	ids := make(map[string]bool, len(prd.UserStories))
	for _, s := range prd.UserStories {
		if s.ID == "" {
			return fmt.Errorf("story with priority %d has empty ID", s.Priority)
		}
		if ids[s.ID] {
			return fmt.Errorf("duplicate story ID: %s", s.ID)
		}
		ids[s.ID] = true
	}

	// Check sequential priorities (1, 2, 3, ...).
	sorted := make([]UserStory, len(prd.UserStories))
	copy(sorted, prd.UserStories)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	for i, s := range sorted {
		expected := i + 1
		if s.Priority != expected {
			return fmt.Errorf("non-sequential priority: story %s has priority %d, expected %d", s.ID, s.Priority, expected)
		}
	}

	return nil
}

// JSONSchema is the JSON schema string for the PRD structure, suitable for
// use with claude --json-schema.
const JSONSchema = `{
  "type": "object",
  "required": ["project", "branchName", "description", "userStories"],
  "properties": {
    "project": {
      "type": "string",
      "description": "Project name identifier"
    },
    "branchName": {
      "type": "string",
      "description": "Git branch name for this feature"
    },
    "description": {
      "type": "string",
      "description": "High-level description of the project"
    },
    "userStories": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "title", "description", "acceptanceCriteria", "priority", "passes", "notes"],
        "properties": {
          "id": {
            "type": "string",
            "description": "Unique story identifier (e.g. US-001)"
          },
          "title": {
            "type": "string",
            "description": "Short title for the user story"
          },
          "description": {
            "type": "string",
            "description": "User story description in 'As a... I want... so that...' format"
          },
          "acceptanceCriteria": {
            "type": "array",
            "items": { "type": "string" },
            "description": "List of acceptance criteria for the story"
          },
          "priority": {
            "type": "integer",
            "description": "Priority order (1 = highest)"
          },
          "passes": {
            "type": "boolean",
            "description": "Whether the story has been completed and passes verification"
          },
          "notes": {
            "type": "string",
            "description": "Additional implementation notes"
          }
        }
      }
    }
  }
}`
