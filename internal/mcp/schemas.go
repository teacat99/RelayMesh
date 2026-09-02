package mcp

import (
	"encoding/json"

	"github.com/teacat99/RelayMesh/internal/model"
)

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
				"description": "Unique workflow identifier to group multiple interaction turns under a single sidebar thread (e.g. 'wf-20260827-auth-refactor' or 'relaymesh-optimization'). Providing a new workflow_id automatically creates a new workflow topic in the sidebar; providing an existing workflow_id appends a new conversational turn into that workflow topic. If unknown after context compression, call list_sessions with project_directory and group_by='workflow' to discover recent workflows."
			},
			"phase": {
				"type": "string",
				"description": "Optional: report the current workflow phase (e.g. 'assess', 'plan', 'dev', 'verify', 'done'). The phase is displayed in the Web UI as a progress slider and injected into every MCP response header as current_phase. If omitted, the phase remains unchanged."
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
			"project_directory": {
				"type": "string",
				"description": "Filter sessions by project directory path. Useful for discovering workflows after context compression."
			},
			"host_name": {
				"type": "string",
				"description": "Filter sessions by host name."
			},
			"group_by": {
				"type": "string",
				"enum": ["workflow"],
				"description": "When set to 'workflow', returns aggregated workflow-level summaries instead of individual sessions. Includes workflow_id, title, host_name, session_count, last_active_at, etc."
			},
			"limit": {
				"type": "integer",
				"default": 20,
				"description": "Max sessions (or workflows when group_by is set) to return."
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

	workflowContextSchema = json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["checkpoint_save", "checkpoint_get", "checkpoint_list", "checkpoint_verify", "note_save", "note_get", "note_list", "note_update", "note_delete", "list_workflows", "set_phase"],
				"description": "Action to perform on the workflow context store."
			},
			"workflow_id": {
				"type": "string",
				"description": "Target workflow identifier. Required for all checkpoint and note actions (except list_workflows)."
			},
			"checkpoint": {
				"type": "object",
				"description": "Structured checkpoint content for checkpoint_save. Must contain a 'basis' object with at least one anchor (task_revision, report_sequence, feedback_sequence, or git_commit).",
				"properties": {
					"basis": {
						"type": "object",
						"properties": {
							"task_revision": { "type": "integer" },
							"report_sequence": { "type": "integer" },
							"feedback_sequence": { "type": "integer" },
							"git_commit": { "type": "string" }
						}
					},
					"objective": { "type": "string" },
					"acceptance_criteria": { "type": "array", "items": { "type": "string" } },
					"current_stage": {
						"type": "object",
						"properties": {
							"id": { "type": "string" },
							"status": { "type": "string" }
						}
					},
					"completed": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"claim": { "type": "string" },
								"evidence": { "type": "array", "items": { "type": "string" } }
							}
						}
					},
					"decisions": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"decision_id": { "type": "string" },
								"status": { "type": "string" },
								"summary": { "type": "string" },
								"supersedes": { "type": "string" },
								"source": { "type": "string" }
							}
						}
					},
					"in_progress": { "type": "array", "items": { "type": "string" } },
					"next_actions": { "type": "array", "items": { "type": "string" } },
					"blockers": { "type": "array", "items": { "type": "string" } },
					"open_questions": { "type": "array", "items": { "type": "string" } },
					"constraints": { "type": "array", "items": { "type": "string" } },
					"changed_files": { "type": "array", "items": { "type": "string" } },
					"verification": { "type": "array", "items": { "type": "string" } },
					"unknowns": { "type": "array", "items": { "type": "string" } },
					"resume_instruction": { "type": "string" }
				}
			},
			"revision": {
				"type": "integer",
				"description": "Checkpoint revision number. Used for checkpoint_verify and targeted retrieval."
			},
			"session_id": {
				"type": "string",
				"description": "Session identifier for session-bound notes."
			},
			"note_key": {
				"type": "string",
				"description": "Stable key for upsert-style note operations. Same workflow_id + note_key will update existing note."
			},
			"note_id": {
				"type": "integer",
				"description": "Note ID for note_get, note_update, note_delete by ID."
			},
			"content": {
				"type": "string",
				"description": "Note content for note_save or note_update."
			},
			"limit": {
				"type": "integer",
				"description": "Max items to return for list operations. Default 20."
			},
			"replay_after_checkpoint": {
				"type": "boolean",
				"description": "If true, checkpoint_get also returns recent reports/feedback deltas after the checkpoint."
			},
			"max_bytes": {
				"type": "integer",
				"description": "Soft limit on response size in bytes for checkpoint_get."
			},
			"phase_id": {
				"type": "string",
				"description": "Phase ID for set_phase action (e.g. 'assess', 'plan', 'dev', 'verify', 'done')."
			}
		}
	}`)

	manageSkillsSchema = json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "get", "create", "update", "delete"],
				"description": "CRUD action to perform on the external skill directory."
			},
			"name": {
				"type": "string",
				"description": "Skill identifier (kebab-case, e.g. 'git-convention'). Required for get/create/update/delete."
			},
			"summary": {
				"type": "string",
				"description": "Short description (max 500 chars). Required for create, optional for update."
			},
			"content": {
				"type": "string",
				"description": "Full Markdown content (max 20000 chars). Required for create, optional for update."
			},
			"is_active": {
				"type": "boolean",
				"description": "Whether this skill is active. Active skills have their summary injected into every MCP response context header. Default: true."
			}
		}
	}`)
)

func GetToolDefinitionsForPermissions(perms model.Permissions) []ToolDefinition {
	var tools []ToolDefinition

	if perms.Configure {
		tools = append(tools, ToolDefinition{
			Name: "configure_task",
			Description: `Commander/orchestrator-side tool for managing autopilot tasks.

Actions: create, update, set_wait_policy, send_feedback, get, list, list_updates, read_reports, ack_reports, close.

Usage contract:
- This tool is for the COMMANDER (orchestrating AI or human admin), NOT the executor.
- Use "create" with segments to define task stages for an executor to follow.
- Use "send_feedback" to relay human directives or commander instructions to the executor.
- Use "read_reports" + "ack_reports" to consume executor progress reports.
- Use "set_wait_policy" to configure how long the executor should wait between feedback checks.
- Optimistic locking: provide expected_revision when updating to prevent conflicts.
- Do NOT use this tool if you are an executor agent — use report_progress instead.`,
			InputSchema: configureTaskSchema,
		})
	}

	if perms.Execute {
		tools = append(tools, ToolDefinition{
			Name: "report_progress",
			Description: `Executor-side tool for autopilot/external-orchestration mode. Use this when operating under a task assigned via configure_task.

Actions:
- "sync": MUST be your first call when starting or resuming a task. Returns current task segments, feedback, and revision.
- "report": Submit progress updates. Required "kind" values:
  · "progress" — incremental progress update
  · "stage" — stage/phase completion milestone
  · "evidence" — deliverable artifact or verification result
  · "question" — blocked and need external decision (especially for irreversible actions)
  · "completion" — final completion report (does NOT mean acceptance; wait for external confirmation)
- "check_feedback": Non-blocking check for new feedback from commander/human. Respect the returned wait policy timing.

Mutual exclusion: Do NOT call interactive_feedback while operating in autopilot mode. All communication goes through report_progress.
On MCP errors: If report_progress calls fail repeatedly, degrade to away mode (stop proactive communication, continue authorized work, defer irreversible actions).`,
			InputSchema: reportProgressSchema,
		})
	}

	if perms.Feedback {
		tools = append(tools, ToolDefinition{
			Name: "interactive_feedback",
			Description: `PRIMARY communication channel with the user via Web UI. All substantive content (analysis, plans, code change summaries, questions) MUST go through this tool's summary parameter.

When to call:
- Before starting new work (understanding/plan/risks)
- At ambiguity or major decisions
- After completing each phase/module
- For final session summary

Summary rules:
- MUST be substantive Markdown (headings, lists, code fences). Empty strings and placeholder phrases are rejected.
- Write in the user's language (Chinese for Chinese-speaking users).
- Include: what you did/plan to do, file paths touched, explicit questions for the user.

Workflow_id:
- Provide a consistent workflow_id across a conversation to group turns into a sidebar thread.
- If workflow_id is unknown (e.g. after context compression), call list_sessions({ project_directory: "...", group_by: "workflow" }) first to discover recent workflows for this project, then use the discovered workflow_id.

Host name:
- host_name is determined server-side (from UI settings, credential binding, environment, or workflow inheritance). Do NOT attempt to set it via parameters.

Response header context:
- Every response includes a 'recent_workflows' field listing active workflows for the same project, which helps recover context after compression.

Prohibitions:
- Do NOT use AskQuestion when this tool is available. Route all questions through summary.
- Do NOT end the turn with plain chat text when there is substantive content for the user.
- Do NOT call this tool when user_presence is "away" or "autopilot" — use report_progress for autopilot, or silently continue for away.
- Do NOT treat keepalive/wait responses as user feedback. Only "=== 用户反馈 ===" contains actual user input.`,
			InputSchema: interactiveFeedbackSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "continue_feedback_session",
			Description: `Continue polling for an existing feedback session without creating a new one.

Typical flow:
1. interactive_feedback returns "=== 等待回执 ===" (keepalive timeout, no user input yet)
2. Follow the wait instructions in the response (usually: AwaitShell for N minutes)
3. Call continue_feedback_session with the same workflow_id
4. Check the === marker in the response to determine next action

Response markers and their meanings:
- "=== 用户反馈 ===" — User has responded. Extract and act on their feedback.
- "=== 等待回执 ===" — Still waiting. Follow the embedded wait instructions again.
- "=== 反馈超时 ===" — Max wait exceeded. Follow the instructions in the response body (configured by user via flowPrompts).
- "=== 用户暂离 ===" — Away/safety mode. Follow the instructions in the response body.
- "=== 托管自驾 ===" — Autopilot mode. Follow the instructions in the response body.
- "=== 取消反馈 ===" — User cancelled. Re-ask for their new goal via interactive_feedback.

Important: Keepalive responses ("=== 等待回执 ===") are NOT user feedback. Do not interpret or act on them as user instructions.
Prefer workflow_id over session_id — the server auto-locates the latest pending session in that workflow.`,
			InputSchema: continueFeedbackSessionSchema,
		})
	}

	if perms.Sessions {
		tools = append(tools, ToolDefinition{
			Name:        "list_sessions",
			Description: "Query feedback sessions filtered by status, workflow_id, project_directory, or host_name. Use group_by='workflow' to get workflow-level summaries — essential for recovering workflow_id after context compression. Example: list_sessions({ project_directory: '/path/to/project', group_by: 'workflow' }) to discover all workflows for this project.",
			InputSchema: listSessionsSchema,
		})
		tools = append(tools, ToolDefinition{
			Name:        "get_session_history",
			Description: "Retrieve multi-round conversation history and user feedback text for a workflow or session. Use to review prior context when resuming a conversation.",
			InputSchema: getSessionHistorySchema,
		})
	}

	if perms.SystemInfo {
		tools = append(tools, ToolDefinition{
			Name:        "get_system_info",
			Description: "Get environment and system information.",
			InputSchema: getSystemInfoSchema,
		})
	}

	if perms.Skills {
		tools = append(tools, ToolDefinition{
			Name: "manage_skills",
			Description: `External skill directory — user-defined norms, conventions, and reusable instructions stored in RelayMesh.

Actions:
- "list": List all skills with name, summary, and is_active status. Use to discover available norms.
- "get": Retrieve the full Markdown content of a skill by name. Use when you need the complete specification.
- "create": Create a new skill. Requires name, summary, content. is_active defaults to true.
- "update": Update an existing skill. Provide name plus any fields to change (summary, content, is_active).
- "delete": Remove a skill by name.

Active skills (is_active=true) have their summaries automatically injected into every MCP response context header, ensuring persistent availability across context compression. Full content is loaded on-demand via "get".

Limits: name max 128 chars, summary max 500 chars, content max 20000 chars, max 50 skills total.`,
			InputSchema: manageSkillsSchema,
		})
	}

	if perms.Feedback || perms.Execute {
		tools = append(tools, ToolDefinition{
			Name: "workflow_context",
			Description: `Verifiable task recovery capsule and AI notes — persist structured workflow state to survive context compression.

## Checkpoint Actions (Recovery Capsule)
- "checkpoint_save": Save a structured checkpoint with basis anchors (task_revision, report_sequence, feedback_sequence, git_commit), objective, decisions, completed claims with evidence, next actions, blockers, and resume instructions. Each save auto-increments the revision. Old checkpoints are marked as superseded.
- "checkpoint_get": Retrieve the latest checkpoint for a workflow. Set replay_after_checkpoint=true to also get recent deltas. Set max_bytes to limit response size.
- "checkpoint_list": List historical checkpoints for a workflow.
- "checkpoint_verify": Check if a checkpoint is still fresh (compares basis anchors against current task state). Returns freshness status and any drift reasons.

## Note Actions (AI Notes)
- "note_save": Create or update a note. Provide workflow_id + note_key for upsert (same key overwrites). Provide session_id to bind to a specific session. Notes are editable free-form text for recording thinking fragments, findings, and investigation trails.
- "note_get": Retrieve a note by note_id or by workflow_id + note_key.
- "note_list": List notes filtered by workflow_id and/or session_id.
- "note_update": Update note content by note_id.
- "note_delete": Delete a note by note_id.

## Discovery
- "list_workflows": List known workflows with their latest checkpoint info, session count, current phase, and last activity time. Essential for recovering context after compression — equivalent to an _index.md for workflows.

## Phase Control
- "set_phase": Update the current workflow phase without triggering a full feedback interaction. Requires workflow_id and phase_id. The phase change is broadcast via SSE and injected into subsequent MCP response headers as current_phase + phase_prompt.

## Key Principles
- Checkpoints are append-only (never overwrite history); original records are preserved.
- Notes do NOT replace original session history — they are supplementary thinking memos.
- Always include basis anchors in checkpoints so freshness can be verified.
- Save checkpoints at semantic boundaries: after key decisions, stage completions, before pauses, and before completion reports.`,
			InputSchema: workflowContextSchema,
		})
	}

	return tools
}
