package mcp

import "encoding/json"

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var (
	configureTaskSchema = json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "update", "set_wait_policy", "send_feedback", "get", "list", "list_updates", "read_reports", "ack_reports", "close"],
				"description": "Configure action to perform"
			},
			"task_id": { "type": "string", "description": "Target task identifier" },
			"expected_revision": { "type": "integer", "description": "Expected task revision for optimistic locking" },
			"mode": { "type": "string", "enum": ["replace", "patch"], "description": "Update mode for segment content" },
			"segment": { "type": "string", "description": "Segment name to update" },
			"old_text": { "type": "string", "description": "Old text to replace in patch mode" },
			"new_text": { "type": "string", "description": "New segment content" },
			"body": { "type": "string", "description": "Feedback or report message content" },
			"references": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["path"],
					"properties": {
						"path": { "type": "string" },
						"line": { "type": "integer" },
						"description": { "type": "string" }
					}
				}
			},
			"segments": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["name", "content"],
					"properties": {
						"name": { "type": "string" },
						"content": { "type": "string" },
						"position": { "type": "integer" }
					}
				}
			},
			"wait_policy": {
				"type": "object",
				"properties": {
					"after_minutes": { "type": "integer" },
					"max_no_feedback_checks": { "type": "integer" },
					"wait_instruction": { "type": "string" },
					"exhausted_instruction": { "type": "string" }
				}
			},
			"idempotency_key": { "type": "string" },
			"state": { "type": "string" },
			"cursor": { "type": "string" },
			"limit": { "type": "integer" },
			"after_report_sequence": { "type": "integer" },
			"through_report_sequence": { "type": "integer" }
		}
	}`)

	reportProgressSchema = json.RawMessage(`{
		"type": "object",
		"required": ["action", "task_id"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["sync", "report", "check_feedback"],
				"description": "Report action to perform"
			},
			"task_id": { "type": "string", "description": "Target task identifier" },
			"known_task_revision": { "type": "integer" },
			"acknowledged_task_revision": { "type": "integer" },
			"after_feedback_sequence": { "type": "integer" },
			"acknowledged_feedback_sequence": { "type": "integer" },
			"kind": { "type": "string", "enum": ["progress", "stage", "evidence", "question", "completion"] },
			"body": { "type": "string" },
			"references": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["path"],
					"properties": {
						"path": { "type": "string" },
						"line": { "type": "integer" },
						"description": { "type": "string" }
					}
				}
			},
			"idempotency_key": { "type": "string" }
		}
	}`)

	interactiveFeedbackSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"project_directory": {
				"type": "string",
				"default": ".",
				"description": "Project directory path."
			},
			"host_name": {
				"type": "string",
				"default": "",
				"description": "Optional host name identifier (e.g. 'wsl', 'macbook', 'dev-server') to distinguish sessions and project paths from different machines or containers."
			},
			"summary": {
				"type": "string",
				"default": "",
				"description": "Markdown summary of the AI's current work / plan / question, rendered in the Web UI."
			},
			"title": {
				"type": "string",
				"default": "",
				"description": "Optional short headline (max ~30 chars) for this feedback round."
			},
			"workflow_id": {
				"type": "string",
				"default": "",
				"description": "Unique workflow identifier to group multiple interaction turns under a single sidebar thread (e.g. 'wf-20260827-auth-refactor' or 'relaymesh-optimization'). Providing a new workflow_id automatically creates a new workflow topic in the sidebar; providing an existing workflow_id appends a new conversational turn into that workflow topic."
			}
		}
	}`)

	continueFeedbackSessionSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {
				"type": "string",
				"description": "Target Web feedback session id (e.g. 'sess-a558c09c'). Optional if workflow_id is provided."
			},
			"workflow_id": {
				"type": "string",
				"description": "Optional workflow identifier to automatically continue/poll the latest pending turn of that workflow."
			}
		}
	}`)

	getSystemInfoSchema = json.RawMessage(`{
		"type": "object",
		"properties": {}
	}`)

	listSessionsSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"status": {
				"type": "string",
				"enum": ["all", "pending", "completed", "cancelled"],
				"description": "Filter sessions by status."
			},
			"workflow_id": {
				"type": "string",
				"description": "Optional workflow identifier to filter sessions."
			},
			"limit": {
				"type": "integer",
				"default": 20,
				"description": "Max sessions to return."
			}
		}
	}`)

	getSessionHistorySchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"workflow_id": {
				"type": "string",
				"description": "Target workflow identifier to retrieve full conversational history."
			},
			"session_id": {
				"type": "string",
				"description": "Optional target session ID."
			}
		}
	}`)
)

func GetToolDefinitions(role string) []ToolDefinition {
	switch role {
	case "configure":
		return []ToolDefinition{
			{
				Name:        "configure_task",
				Description: "Create/modify tasks, segments, wait policies, send feedback, and read/acknowledge reports.",
				InputSchema: configureTaskSchema,
			},
		}
	case "execute":
		return []ToolDefinition{
			{
				Name:        "report_progress",
				Description: "Sync task segments/rules, submit progress reports, and check for feedback non-blockingly.",
				InputSchema: reportProgressSchema,
			},
		}
	default:
		return []ToolDefinition{
			{
				Name:        "interactive_feedback",
				Description: "Open a Web UI to collect interactive feedback from the user. Primary human communication channel.",
				InputSchema: interactiveFeedbackSchema,
			},
			{
				Name:        "continue_feedback_session",
				Description: "Continue waiting for an existing feedback session without creating a new one.",
				InputSchema: continueFeedbackSessionSchema,
			},
			{
				Name:        "list_sessions",
				Description: "Query list of feedback sessions filtered by status or workflow_id.",
				InputSchema: listSessionsSchema,
			},
			{
				Name:        "get_session_history",
				Description: "Retrieve multi-round conversation history and user feedback text for a workflow or session.",
				InputSchema: getSessionHistorySchema,
			},
			{
				Name:        "get_system_info",
				Description: "Get environment and system information.",
				InputSchema: getSystemInfoSchema,
			},
			{
				Name:        "configure_task",
				Description: "Create/modify tasks, segments, wait policies, send feedback, and read/acknowledge reports.",
				InputSchema: configureTaskSchema,
			},
			{
				Name:        "report_progress",
				Description: "Sync task segments/rules, submit progress reports, and check for feedback non-blockingly.",
				InputSchema: reportProgressSchema,
			},
		}
	}
}
