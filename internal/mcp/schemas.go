package mcp

import (
	"encoding/json"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

type ToolDefinition struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	InputSchema     json.RawMessage `json:"inputSchema"`
	SecuritySchemes json.RawMessage `json:"securitySchemes,omitempty"`
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
		"required": ["workflow_id", "summary"],
		"properties": {
			"workflow_id": {
				"type": "string",
				"description": "[CRITICAL 1/3: THREAD IDENTIFIER] Required. Unique workflow identifier to group multiple interaction turns under a single sidebar thread (e.g. 'relaymesh-continuation'). In multi-turn chat, you MUST retain the existing workflow_id from previous turns and context. BANNED: Never invent new workflow IDs, never change workflow ID mid-chat, and never omit this parameter!"
			},
			"title": {
				"type": "string",
				"default": "",
				"description": "[CRITICAL 2/3: SHORT HEADLINE ONLY] Concise 5-15 word one-line title for this feedback round (e.g. '图片加载优化方案确认'). BANNED: Never put markdown blocks, lists, code fences, or multiline text here!"
			},
			"summary": {
				"type": "string",
				"description": "[CRITICAL 3/3: FULL MARKDOWN BODY] Required. The comprehensive markdown content (C-PLAN analysis, architecture proposals, code change summaries, verification results, or questions). Must be substantive markdown with headings and lists. BANNED: Never put a short title here, and never put detailed body content into 'title'! References in the body become clickable links in the Web UI when the target exists: task #N, confirm item Q<N>, doc [[path]] (.md optional; [[path#anchor|label]]), session sess-xxxxxxxx; they stay plain text inside code fences."
			},
			"content": {
				"type": "string",
				"description": "Alternative alias for summary (accepted for backward compatibility)."
			},
			"project_directory": {
				"type": "string",
				"description": "[REQUIRED ABSOLUTE PATH] The exact absolute workspace directory of the client project where the agent is running (e.g. '/home/user/RelayMesh'). Do NOT use relative path '.' as the server may be hosted on a different remote machine."
			},
			"phase": {
				"type": "string",
				"description": "Optional: report the current workflow phase: 'discuss' (quick information exchange, read-only), 'plan' (fact-based solution design, read-only) or 'dev' (implementation, verification and wrap-up). Legacy ids are still accepted and mapped: 'assess' -> 'discuss', 'verify' / 'done' -> 'dev'. The phase is displayed in the Web UI as a progress slider and injected into every MCP response header as current_phase. If omitted, the phase remains unchanged."
			},
			"commit": {
				"type": "string",
				"description": "Optional delivery anchor: the output of ` + "`git log -1 --format='%h %s'`" + ` run in project_directory (e.g. '57bb6d1 docs: add anchor plan'). The server stamps it with the delivery time, shows it as a chip in the Web UI, and echoes the workflow's latest anchor in every response header as 'anchor:', so a reset context (or the human) can judge how stale this message is and whether new commits landed since. Omit it when the directory is not a git repository. Never invent a hash."
			},
			"wait": {
				"type": "boolean",
				"description": "Async dialog mode only. Takes effect ONLY when the latest receipt header says 'mode: async'. Pass false to publish this message as a non-blocking progress/answer notice and keep working instead of waiting for a reply. In every other situation the parameter is ignored: the call blocks as usual and the receipt explains why. Never use it to get approval for an irreversible action (deploy, push, delete data, credentials): a notice cannot be approved. In async mode send an async_ask card with risk=L2 instead and do not execute the step until the user explicitly approves it; in sync mode block with interactive_feedback."
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

	getSessionImageSchema = json.RawMessage(`{
		"type": "object",
		"required": ["session_id"],
		"properties": {
			"session_id": {
				"type": "string",
				"description": "Target feedback session id (e.g. 'sess-a558c09c'), or 'inbox-<number>' for the images of an appended inbox message."
			},
			"image_index": {
				"type": "integer",
				"default": 0,
				"description": "0-based index of the attached image (e.g. 0 for image #1, 1 for image #2). Default is 0."
			},
			"output_mode": {
				"type": "string",
				"enum": ["image", "base64"],
				"default": "image",
				"description": "Output mode. 'image' returns native MCP ImageContent (for multimodal models like Claude/GPT-4o/Gemini); 'base64' returns structured JSON with image metadata and base64 string for saving to local disk."
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
			"project_directory": {
				"type": "string",
				"description": "[REQUIRED unless workflow_id is provided] Absolute project workspace directory on the client host (e.g. '/home/user/RelayMesh'). Enforces strict project boundary isolation to prevent pulling unrelated context from other projects."
			},
			"workflow_id": {
				"type": "string",
				"description": "Optional workflow identifier to filter sessions. When provided (e.g. by explicit human instruction), allows viewing history for that specific workflow."
			},
			"status": {
				"type": "string",
				"enum": ["all", "pending", "completed", "cancelled"],
				"description": "Filter sessions by status."
			},
			"host_name": {
				"type": "string",
				"description": "Filter sessions by host name (automatically defaults to current client host from credential)."
			},
			"group_by": {
				"type": "string",
				"enum": ["workflow"],
				"description": "When set to 'workflow', returns aggregated workflow-level summaries for the project instead of individual sessions."
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
				"enum": ["checkpoint_save", "checkpoint_get", "checkpoint_list", "checkpoint_verify", "note_save", "note_get", "note_list", "note_update", "note_delete", "list_workflows", "set_phase", "session_doc_save", "session_doc_get", "task_create", "task_update", "task_get", "task_list", "inbox", "inbox_peek", "inbox_ack", "wake_permit"],
				"description": "Action to perform on the workflow context store."
			},
			"workflow_id": {
				"type": "string",
				"description": "Target workflow identifier. Required for all checkpoint, note and task actions (except list_workflows)."
			},
			"checkpoint": {
				"type": "object",
				"description": "Structured checkpoint content for checkpoint_save. The optional 'basis' object may carry task_revision, report_sequence, feedback_sequence, or git_commit; when git_commit is omitted the server fills it with the workflow's latest delivery anchor and records which delivery it came from.",
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
				"description": "Phase ID for set_phase action: 'discuss', 'plan' or 'dev'. Legacy ids are still accepted and mapped ('assess' -> 'discuss', 'verify' / 'done' -> 'dev'); the receipt reports the phase that actually took effect."
			},
			"project_directory": {
				"type": "string",
				"description": "Client workspace absolute path (e.g. '/home/user/RelayMesh'). Required for list_workflows to scope workflow summaries strictly to the current project."
			},
			"task_id": {
				"type": "integer",
				"description": "Task number within the workflow (the 12 in '#12'). Required for task_update and task_get. Numbers are never reused."
			},
			"title": {
				"type": "string",
				"description": "Task title, one line, max 200 characters. Required for task_create; optional rename for task_update."
			},
			"kind": {
				"type": "string",
				"enum": ["bug", "feature", "improve", "research", "ops"],
				"description": "Task kind: bug (defect), feature, improve (improvement / refactor), research (investigation & design), ops (operations). Default feature on task_create. For task_list it filters by kind."
			},
			"state": {
				"type": "string",
				"enum": ["todo", "doing", "review", "done", "shelved"],
				"description": "Task state, bound to YOUR development flow: todo -> doing (building) -> review (verifying: regression / e2e / deploy smoke) -> done. Set done yourself once everything passes, with a conclusion sent in the same call or earlier: the server rejects done without one (the conclusion is the evidence: what was done and how it was verified). shelved is the side branch. The user does not confirm tasks one by one any more; when they find a problem they tell you in the session or use the task's improve action, which puts it back to doing."
			},
			"states": {
				"type": "array",
				"items": { "type": "string", "enum": ["todo", "doing", "review", "done", "shelved"] },
				"description": "task_list filter. Default: todo, doing and review (the open tasks)."
			},
			"labels": {
				"type": "array",
				"items": { "type": "string" },
				"description": "Free labels for the task, e.g. ['terminal', 'settings']. Max 8, each max 24 characters. On task_update the list REPLACES the old one; [] clears it."
			},
			"refs": {
				"type": "array",
				"items": { "type": "string" },
				"description": "References that help find the work later: 'commit:abc1234', a decision id like 'D-201', a file path, a doc link. Max 10, each max 200 characters. On task_update the list REPLACES the old one."
			},
			"description": {
				"type": "string",
				"description": "task_create / task_update: what this task IS — the background, what is to be done and what counts as done, one short paragraph, max 1000 characters. The title is only a name and progress only says where it stands, so write this once when you create a task (it is what the user reads on the Web task page and in the task dialog). Editing it is not progress: it does not touch the timeline or the last-progress time. On task_update '' clears it. Task #N and doc [[path]] references written here become clickable links in the task dialog."
			},
			"progress": {
				"type": "string",
				"description": "Latest progress summary, max 500 characters. OVERWRITES the previous one (the history is kept in the timeline). Say where it stands now and what is next, not a changelog."
			},
			"conclusion": {
				"type": "string",
				"description": "1-3 line outcome, max 400 characters. Required when you set a task to done (what was done and how it was verified: tests / e2e / deploy smoke); also write it when you shelve a task. After months it is what remains of the task."
			},
			"note": {
				"type": "string",
				"description": "A short remark appended to the task timeline (max 500 characters), e.g. a decision or a blocker. Optional on task_create / task_update. Task #N and doc [[path]] references in it become clickable links in the task timeline."
			},
			"parent_task_id": {
				"type": "integer",
				"description": "Task number of a parent batch task in the same workflow (one level only). On task_update 0 detaches the task from its parent."
			},
			"query": {
				"type": "string",
				"description": "task_list text filter over title, labels, progress and conclusion."
			},
			"include_closed": {
				"type": "boolean",
				"description": "task_list: also return done and shelved tasks (ignored when 'states' is given)."
			},
			"module": {
				"type": "string",
				"description": "task_create / task_update: module path the task belongs to, e.g. '终端' or '终端/历史日志'. It must already exist (define modules with task_board action=plan; the error lists the existing paths). On task_update '' removes the task from its module."
			},
			"percent": {
				"type": "integer",
				"description": "task_create / task_update: completion 0-100 shown as the progress bar on the Web task page. Only set it when you have a believable number; -1 clears it (the bar is then derived from state / sub-tasks)."
			},
			"stage": {
				"type": "string",
				"enum": ["assess", "plan", "dev", "improve", ""],
				"description": "task_create / task_update: what kind of work the task is in right now — assess (deciding whether / what to do) -> plan (deciding how) -> dev (building it) -> improve (iterating after delivery). Independent of state and kind; the Web task page shows it as the first of two progress bars. Leave it out when unsure; on task_update '' clears it."
			},
			"assignee": {
				"type": "string",
				"description": "Shared project task board. task_create / task_update: hand the task to another workflow of the SAME project board (the assignee's workflow_id; on task_update '' withdraws the assignment). Only a management agent or the user may assign; ordinary agents only write their own tasks. task_list: filter — 'me' (assigned to you), 'none' (unassigned) or a workflow_id."
			},
			"claim": {
				"type": "string",
				"enum": ["accept", "decline", "deliver", "accept_delivery", "reject_delivery"],
				"description": "task_update: a collaboration step on an assigned task. accept = the assignee takes an offered task (todo -> doing); decline = the assignee refuses an offered task (note = the reason, required); deliver = the assignee hands the work over (state -> review; note / progress / conclusion must say what was done and how it was verified); accept_delivery = the assigner or a management agent accepts a delivered task (state -> done, needs a conclusion); reject_delivery = the assigner sends it back (state -> doing; note = the reason, required). Setting state=doing / review yourself on a task assigned to you has the same effect as accept / deliver. Cannot be combined with 'assignee'."
			},
			"ids": {
				"type": "array",
				"items": { "type": "integer" },
				"description": "inbox_ack: acknowledge only these appended-message numbers (the 12 in '追加 #12'). Omit to acknowledge everything you have already read."
			},
			"reminder": {
				"type": "boolean",
				"description": "inbox_peek: set by editor hooks that ask whether to remind the agent. The server then applies the reminder schedule (right away, then 5 and 15 minutes later, at most 3 times per message) and records the reminder. Agents themselves do not need this."
			},
			"recent": {
				"type": "boolean",
				"description": "inbox: also list the messages you already acknowledged in the last day (useful after a context reset)."
			},
			"source": {
				"type": "string",
				"enum": ["hook", "check"],
				"description": "inbox_peek: set by the editor hook script ('hook') or its install check ('check'); neither counts as the agent calling RelayMesh. Agents themselves do not need this."
			},
			"agent_event": {
				"type": "string",
				"enum": ["tool", "stop", "session_start", "session_end", "prompt", "thought", "tool_start", "tool_end", "tool_fail", "response", "compact"],
				"description": "inbox_peek: set by the editor hook script only — what just happened in the editor: 'tool' / 'tool_end' (a tool call finished), 'tool_start', 'tool_fail', 'thought', 'response', 'prompt' (a turn began), 'stop' (a turn ended), 'session_start' / 'session_end', 'compact'. The agent-state tracker and the session guardian use it to tell a working agent from a stalled one. Agents themselves do not need this."
			},
			"agent_status": {
				"type": "string",
				"enum": ["completed", "aborted", "error", "closed"],
				"description": "inbox_peek with agent_event 'stop': how the turn ended; with 'session_end': why the session ended. Set by the editor hook script only. Agents themselves do not need this."
			},
			"agent_tool": {
				"type": "string",
				"description": "inbox_peek with agent_event 'tool_start' / 'tool_end' / 'tool_fail': the tool's name only (never its arguments). Set by the editor hook script only."
			},
			"agent_ts": {
				"type": "integer",
				"description": "inbox_peek: when the hook event happened, in Unix milliseconds on the editor machine's clock. Set by the editor hook script only."
			},
			"agent_duration_ms": {
				"type": "integer",
				"description": "inbox_peek with agent_event 'tool_end': how long the tool call took, in milliseconds. Set by the editor hook script only."
			},
			"client_kind": {
				"type": "string",
				"enum": ["ide", "cli"],
				"description": "inbox_peek: which Cursor client the hook runs in. Set by the editor hook script only."
			},
			"cursor_version": {
				"type": "string",
				"description": "inbox_peek: the Cursor client version, reported by the hook script. Set by the editor hook script only."
			},
			"conversation_id": {
				"type": "string",
				"description": "inbox_peek: the editor's conversation id, reported by the hook script so a dead session can be resumed later. Agents themselves do not need this."
			}
		}
	}`)

	chatSchema = json.RawMessage(`{
		"type": "object",
		"required": ["workflow_id"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "read", "send", "members", "status", "end"],
				"description": "Default read."
			},
			"workflow_id": {"type": "string", "description": "The workflow_id from the receipt header."},
			"conversation_id": {"type": "string", "description": "Optional. Default is your own 1:1 chat (wf:<workflow_id>); a group chat you were added to is grp:<id> (see action list). Only the user creates groups and adds members."},
			"text": {"type": "string", "description": "send: the message (Markdown). Not blocking."},
			"title": {"type": "string", "description": "send: optional short title."},
			"phase": {"type": "string", "enum": ["discuss", "plan", "dev"], "description": "send: report your current phase."},
			"project_directory": {"type": "string", "description": "Optional project directory, same meaning as in interactive_feedback."},
			"limit": {"type": "integer", "description": "read: max messages (default 30, max 100)."},
			"before": {"type": "string", "description": "read: cursor from a previous response; returns older history (both sides) without moving your read position."},
			"after": {"type": "string", "description": "read: cursor; returns newer history without moving your read position."},
			"q": {"type": "string", "description": "read: substring search over the history."},
			"mentions": {"type": "array", "items": {"type": "string"}, "description": "send (group): who to @ - member workflow_ids from action members, \"all\", or \"human\" (the user; notifies them past Do-Not-Disturb). Without mentions the message is not pushed to anyone's inbox."},
			"reply_to": {"type": "string", "description": "send (group): id of the message you reply to (m:<id>)."},
			"artifact_ref": {"type": "string", "description": "send: a doc or Artifact in the project library (path or id); the message carries a card the user can open in a side preview. You can also write [[artifact:<doc id>]] in the text yourself."},
			"idem_key": {"type": "string", "description": "send (group): your own unique key for this message; retrying with the same key never posts twice."},
			"since_seq": {"type": "integer", "description": "read (group): read after this sequence number instead of your read cursor."},
			"peek": {"type": "boolean", "description": "read (group): look without moving your read cursor."},
			"targets": {"type": "array", "items": {"type": "string"}, "description": "status (group): member workflow_ids to inspect (default all members)."},
			"reason": {"type": "string", "enum": ["completed", "blocked", "aborted", "handoff"], "description": "end: why you are leaving (default completed)."},
			"summary": {"type": "string", "description": "end: one or two sentences for the user (shown as a system line)."}
		}
	}`)

	asyncAskSchema = json.RawMessage(`{
		"type": "object",
		"required": ["workflow_id"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "get", "list", "withdraw"],
				"description": "Default create."
			},
			"workflow_id": { "type": "string", "description": "The workflow_id from the receipt header." },
			"title": { "type": "string", "description": "create: the question in one line (max 200 chars)." },
			"body": { "type": "string", "description": "create: background the user needs to decide (Markdown, max 20000 chars). For kind=plan: the plan itself. Task #N, confirm item Q<N>, doc [[path]] and session sess-xxxxxxxx references become clickable links in the Web UI when the target exists." },
			"options": {
				"type": "array",
				"minItems": 2,
				"maxItems": 8,
				"description": "create: 2-8 choices. id is optional (a, b, c... are assigned by position). Do not add an 'other / none of the above' choice: the user can always write a custom answer instead (answer.custom).",
				"items": {
					"type": "object",
					"required": ["label"],
					"properties": {
						"id": { "type": "string" },
						"label": { "type": "string" },
						"detail": { "type": "string" },
						"recommended": { "type": "boolean", "description": "Mark your recommendation; the user can adopt all recommendations in one click." }
					}
				}
			},
			"multi": { "type": "boolean", "description": "create: allow selecting several options." },
			"allow_note": { "type": "boolean", "description": "create: let the user add a free-text note." },
			"default_option": { "type": "string", "description": "create: the option id you will assume while waiting. NOT the user's answer; the item stays pending. Not allowed with risk=L2 (the server rejects it): an irreversible step has no default." },
			"risk": { "type": "string", "enum": ["L0", "L1", "L2"], "description": "create: L0 read-only/trivially reversible, L1 reversible change (default), L2 irreversible or high-risk (deploy, push, delete data, credentials, publish): do NOT execute the step until the user explicitly approves it; no answer means not approved. L2 items have no default_option, are never batched or adopted in one click, and never expire." },
			"kind": { "type": "string", "enum": ["ask", "plan"], "description": "create: ask (default) or plan (needs task_id: a plan to approve for that task)." },
			"task_id": { "type": "integer", "description": "create: the task number (the 12 in #12) this item belongs to; required for kind=plan." },
			"doc_path": { "type": "string", "description": "create: a project document this question is about (shown as a link)." },
			"anchor": { "type": "string", "description": "create: heading anchor inside doc_path." },
			"ref": { "type": "string", "description": "get / withdraw: the item reference, e.g. Q12." },
			"status": { "type": "string", "description": "list: pending (default) | answered | dismissed | withdrawn | expired | all." }
		}
	}`)

	projectDocsSchema = json.RawMessage(`{
		"type": "object",
		"required": ["workflow_id"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["overview", "list", "get", "search", "save", "history", "links", "delete", "export", "import", "blueprint", "import_sheet", "module", "grant", "revoke", "grants", "data_get", "data_set", "data_list", "data_delete", "comments", "comment", "comment_resolve"],
				"description": "Default overview. Project documentation library (Markdown, paths, revisions). module = one module page of the project knowledge base (needs scope=project)."
			},
			"workflow_id": { "type": "string" },
			"path": { "type": "string", "description": "Document path (e.g. design/oauth.md). Used by get/save/delete/history." },
			"id": { "type": "string", "description": "Alternative doc ref: seq or numeric id." },
			"section": { "type": "string", "description": "get: return one section; save: replace section when section_body is set." },
			"section_body": { "type": "string" },
			"max_chars": { "type": "integer", "description": "get: chunk size (default 12000)." },
			"next_offset": { "type": "integer" },
			"rev": { "type": "integer", "description": "get/history: specific revision." },
			"since": { "type": "string", "description": "list/search: only docs updated after this RFC3339 time — use it to see what changed since you last looked." },
			"query": { "type": "string", "description": "search / list filter." },
			"body": { "type": "string" },
			"append": { "type": "string" },
			"prepend": { "type": "string" },
			"expected_rev": { "type": "integer" },
			"summary": { "type": "string" },
			"kind": { "type": "string" },
			"status": { "type": "string" },
			"tags": { "type": "array", "items": { "type": "string" } },
			"title": { "type": "string" },
			"kinds": { "type": "array", "items": { "type": "string" } },
			"statuses": { "type": "array", "items": { "type": "string" } },
			"prefix": { "type": "string" },
			"tag": { "type": "string" },
			"limit": { "type": "integer" },
			"paths": { "type": "array", "items": { "type": "string" } },
			"known": { "type": "object", "additionalProperties": { "type": "string" } },
			"max_bytes": { "type": "integer" },
			"cursor": { "type": "string" },
			"base_dir": { "type": "string" },
			"files": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["path", "content"],
					"properties": {
						"path": { "type": "string" },
						"content": { "type": "string" },
						"kind": { "type": "string" },
						"status": { "type": "string" },
						"tags": { "type": "array", "items": { "type": "string" } },
						"title": { "type": "string" }
					}
				}
			},
			"mode": { "type": "string", "enum": ["create", "upsert"] },
			"dry_run": { "type": "boolean" },
			"with_templates": { "type": "boolean" },
			"scope": { "type": "string", "enum": ["workflow", "project"], "description": "Default workflow (this workflow's own docs, exactly as before). project = the whole project knowledge base your workflow belongs to: overview / list / get / search / save / blueprint / module then span every workflow of the project. Fails with a hint when the workflow is not in a project (only the user can create projects)." },
			"module": { "type": "string", "description": "scope=project list/search: only docs bound to this module path (and its sub-modules), e.g. chat/groups. For action=module: the module path." },
			"module_path": { "type": "string", "description": "save: bind the doc to an existing project module (docs under modules/<module>/... bind automatically)." },
			"render": { "type": "string", "enum": ["md", "mermaid", "html"], "description": "save: how the web renders the doc (default md). list/search filter in scope=project." },
			"project_directory": { "type": "string", "description": "Optional. Resolve the project from this directory (with host_name) instead of from the workflow's latest session." },
			"host_name": { "type": "string", "description": "Optional, with project_directory." },
			"grantee": { "type": "string", "description": "grant: the numeric identity id of the agent that receives the permission (see overview / the Web agents page)." },
			"grant_scope": { "type": "string", "enum": ["module", "doc", "workflow"], "description": "grant: what the permission covers." },
			"grant_ref": { "type": "string", "description": "grant: module path, doc path/id, or workflow_id matching grant_scope. For revoke use id = the grant id." },
			"role": { "type": "string", "enum": ["read", "comment", "edit"], "description": "grant: manage cannot be delegated." },
			"expires_hours": { "type": "integer", "description": "grant: optional lifetime in hours (omit = until revoked)." },
			"key": { "type": "string", "description": "data_get / data_set / data_delete: the data key (letters, digits, _ . : -, ≤128 chars)." },
			"value": { "description": "data_set: any JSON value (object, array, string, number, bool, null)." },
			"expected_version": { "type": "integer", "description": "data_set: optimistic concurrency — the version you read (0 = the key must not exist yet); conflicts are rejected so concurrent agents do not overwrite each other." },
			"anchor": { "type": "string", "description": "comment: optional anchor (a heading id or element id) the comment is about." },
			"comment_id": { "type": "integer", "description": "comment_resolve: the id from comments." },
			"resolved": { "type": "boolean", "description": "comment_resolve: true (default) = resolved, false = reopen." },
			"include_resolved": { "type": "boolean", "description": "comments: also list resolved comments." }
		}
	}`)

	taskBoardSchema = json.RawMessage(`{
		"type": "object",
		"required": ["workflow_id"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["overview", "list", "plan", "batch"],
				"description": "Default 'overview'. overview = whole board at a glance (or the how-to when empty); list = filtered tasks; plan = declare the module tree; batch = create / update many tasks in one call."
			},
			"workflow_id": {
				"type": "string",
				"description": "Required. The workflow_id from the receipt header (the same one you pass to interactive_feedback)."
			},
			"session_id": {
				"type": "string",
				"description": "Optional. Your current session_id; tasks you create or update are linked to that session."
			},
			"module": {
				"type": "string",
				"description": "list: only tasks under this module path (a module includes its features), e.g. '终端' or '终端/历史日志'; '未归类' = tasks without a module."
			},
			"states": {
				"type": "array",
				"items": { "type": "string", "enum": ["todo", "doing", "review", "done", "shelved"] },
				"description": "list: states to include. Default: todo, doing and review (the open tasks)."
			},
			"include_closed": {
				"type": "boolean",
				"description": "list: also return done and shelved tasks (ignored when 'states' is given)."
			},
			"kind": {
				"type": "string",
				"enum": ["bug", "feature", "improve", "research", "ops"],
				"description": "list: only this kind."
			},
			"query": {
				"type": "string",
				"description": "list: text filter over title, labels, progress and conclusion."
			},
			"assignee": {
				"type": "string",
				"description": "list: only tasks assigned to this workflow — 'me' (assigned to you), 'none' (unassigned) or a workflow_id."
			},
			"sort": {
				"type": "string",
				"enum": ["plan", "recent"],
				"description": "list: 'plan' (default) follows the module tree order, then doing > review > todo within a module; 'recent' = newest progress first."
			},
			"limit": {
				"type": "integer",
				"description": "list: max tasks to return. Default 30, max 100."
			},
			"modules": {
				"type": "array",
				"description": "plan / batch: the module tree to declare. Each item is a module; 'features' are its sub-items (2 levels max, names must not contain '/'). Existing nodes keep their place; new ones are appended; 'rename' renames a node; 'remove' deletes a node that has no tasks and no features.",
				"items": {
					"type": "object",
					"required": ["name"],
					"properties": {
						"name": { "type": "string", "description": "Module name, one line, max 40 characters." },
						"description": { "type": "string", "description": "One line on what belongs here (max 200). It helps you and the user file tasks consistently." },
						"rename": { "type": "string", "description": "Rename this module to the given name." },
						"remove": { "type": "boolean", "description": "Remove this module (only if empty)." },
						"features": {
							"type": "array",
							"items": {
								"type": "object",
								"required": ["name"],
								"properties": {
									"name": { "type": "string" },
									"description": { "type": "string" },
									"rename": { "type": "string" },
									"remove": { "type": "boolean" }
								}
							}
						}
					}
				}
			},
			"reorder": {
				"type": "boolean",
				"description": "plan / batch: apply the order of 'modules' as given (listed nodes move to the front of their level). Default false: existing nodes keep their position."
			},
			"ops": {
				"type": "array",
				"maxItems": 30,
				"description": "batch: up to 30 operations, applied in order. No task_id / task_ids = create a task (title required). With task_id (or task_ids, to apply the SAME change to several tasks) = update.",
				"items": {
					"type": "object",
					"properties": {
						"task_id": { "type": "integer", "description": "Update this task (the 12 in '#12')." },
						"task_ids": { "type": "array", "items": { "type": "integer" }, "description": "Update all of these tasks with the same change." },
						"title": { "type": "string", "description": "Create: required, one line, max 200. Update (single task only): rename." },
						"kind": { "type": "string", "enum": ["bug", "feature", "improve", "research", "ops"] },
						"stage": { "type": "string", "enum": ["assess", "plan", "dev", "improve", ""], "description": "What the work is right now: assess -> plan -> dev -> improve. Independent of state; leave it out when unsure; on update '' clears it." },
						"state": { "type": "string", "enum": ["todo", "doing", "review", "done", "shelved"], "description": "done needs a conclusion in the same op or already stored: the server rejects done without one. review = verifying." },
						"module": { "type": "string", "description": "Existing module path, e.g. '终端/历史日志'. On update '' removes the task from its module." },
						"percent": { "type": "integer", "description": "0-100, only when believable; -1 clears it." },
						"description": { "type": "string", "description": "What the task IS: background, what to do, what counts as done (max 1000). Write it when you create the task; on update '' clears it. Editing it is not progress (no timeline entry). Task #N and doc [[path]] references in it become clickable links in the task dialog." },
						"progress": { "type": "string", "description": "Where it stands now and what is next (max 500). OVERWRITES the previous progress." },
						"conclusion": { "type": "string", "description": "1-3 line outcome (max 400); write it when a task reaches review / shelved." },
						"note": { "type": "string", "description": "Short remark for the timeline (max 500). Task #N and doc [[path]] references in it become clickable links in the task timeline." },
						"labels": { "type": "array", "items": { "type": "string" } },
						"refs": { "type": "array", "items": { "type": "string" } },
						"parent_task_id": { "type": "integer", "description": "Task number of a parent batch task (one level). 0 detaches." },
						"assignee": { "type": "string", "description": "Shared board: hand the task to another workflow of the same project board (its workflow_id; on update '' withdraws). Only a management agent or the user may assign." },
						"claim": { "type": "string", "enum": ["accept", "decline", "deliver", "accept_delivery", "reject_delivery"], "description": "Update only, on an assigned task: accept / decline (note = reason) / deliver (needs a delivery note) / accept_delivery (assigner; done needs a conclusion) / reject_delivery (assigner; note = reason). Not together with assignee." }
					}
				}
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

// oauthSecuritySchemes 供 ChatGPT 等客户端识别 OAuth 保护的工具。
var oauthSecuritySchemes = json.RawMessage(`[{"type":"oauth2","scopes":["exec","exec:files"]}]`)

func AttachOAuthSecuritySchemes(tools []ToolDefinition) []ToolDefinition {
	for i := range tools {
		tools[i].SecuritySchemes = oauthSecuritySchemes
	}
	return tools
}

func GetToolDefinitionsForPermissions(perms model.Permissions) []ToolDefinition {
	// 必须是非 nil 切片：没有任何工具权限的令牌（例如只带终端权限、而终端工具尚未提供时）
	// 也要返回 "tools": []，而不是 null——部分 MCP 客户端会校验该字段必须是数组。
	tools := make([]ToolDefinition, 0, 12)

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

CORE PARAMETER CONTRACT (CRITICAL):
1. workflow_id (REQUIRED): MUST be provided. In multi-turn chat, ALWAYS retain and pass the workflow_id from previous turns (e.g. 'relaymesh-continuation'). Never omit it, never rename it, and never invent a new ID mid-conversation.
2. title (ONE LINE ONLY): Concise headline (5-15 words). Good: "图片加载优化方案确认". BANNED: Do NOT put markdown reports, multi-paragraph text, or bullet points in title.
3. summary (FULL MARKDOWN BODY): Comprehensive analysis, plans, code changes, or questions. BANNED: Do NOT put a short title here, and do NOT put the body in title.

When to call:
- Before starting new work (understanding/plan/risks)
- At ambiguity or major decisions
- After completing each phase/module
- For final session summary

Summary rules:
- MUST be substantive Markdown (headings, lists, code fences). Empty strings and placeholder phrases are rejected.
- Use natural, fluent, and well-structured Simplified Chinese (简体中文). Avoid telegraphic abbreviations or rigid symbol stacking.
- For complex/multi-step/irreversible tasks, enforce C-PLAN (decisions, decomposition, state flow, invariants, boundaries, alternatives, acceptance) and C-RT self-verification before coding.
- Include: what you did/plan to do, file paths touched, explicit questions for the user.

Workflow_id:
- Provide a consistent workflow_id across a conversation to group turns into a sidebar thread.
- If workflow_id is unknown (e.g. after context compression), call list_sessions({ project_directory: "...", group_by: "workflow" }) first to discover recent workflows for this project, then use the discovered workflow_id.

Host name:
- host_name is determined server-side (from UI settings, credential binding, environment, or workflow inheritance). Do NOT attempt to set it via parameters.

Response header context:
- Every response includes a 'recent_workflows' field listing active workflows for the same project, which helps recover context after compression.

Delivery anchor (optional 'commit' parameter):
- Run ` + "`git log -1 --format='%h %s'`" + ` in project_directory and pass the single-line output as 'commit'. The server records the delivery time itself; you only paste one line.
- Responses echo the workflow's latest anchor as 'anchor: <hash> "<subject>" @ <time>（N 小时前）'. After a context reset, compare it with ` + "`git log <hash>..HEAD`" + ` and ` + "`git status`" + ` to see what changed since that delivery.
- Omit 'commit' when the directory is not a git repository. An unparseable value only yields an 'anchor_warning:' line and never blocks the delivery.

Prohibitions:
- Do NOT use AskQuestion when this tool is available. Route all questions through summary.
- Do NOT end the turn with plain chat text when there is substantive content for the user.
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
- "=== 取消反馈 ===" — User cancelled. Re-ask for their new goal via interactive_feedback.

Important: Keepalive responses ("=== 等待回执 ===") are NOT user feedback. Do not interpret or act on them as user instructions.
Prefer workflow_id over session_id — the server auto-locates the latest pending session in that workflow.`,
			InputSchema: continueFeedbackSessionSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "get_session_image",
			Description: `Retrieve attached image from a feedback session directly via MCP channel.

Essential for isolated or offline sandboxes where the agent cannot make external HTTP requests or download files via URL.

Output Modes:
- "image" (default): Returns native MCP ImageContent block. Multimodal models (Claude, GPT-4o, Gemini) will directly 'see' the image pixels in visual context without downloading.
- "base64": Returns structured JSON containing image metadata (name, format, size_bytes) and base64 string, convenient for saving to disk.

Images that came with an appended inbox message are addressed as session_id "inbox-<number>" (the receipt of workflow_context action "inbox" prints the exact call).`,
			InputSchema: getSessionImageSchema,
		})
	}

	if perms.Sessions {
		tools = append(tools, ToolDefinition{
			Name:        "list_sessions",
			Description: "Query feedback sessions strictly isolated to the current project_directory and host_name. Pass group_by='workflow' to get workflow-level summaries — essential for recovering workflow_id after context compression. Example: list_sessions({ project_directory: '/path/to/project', group_by: 'workflow' }). Cross-project queries are forbidden unless an explicit workflow_id is provided.",
			InputSchema: listSessionsSchema,
		})
		tools = append(tools, ToolDefinition{
			Name:        "get_session_history",
			Description: "Retrieve multi-round conversation history and user feedback text for an explicitly specified workflow_id (or session_id). Use to review prior context when resuming a conversation. The result also carries 'appended_messages': what the user appended while you were working (read-only; ones you read through the inbox / hook / receipt hint never show up in the rounds), and 'appended_retractions': ids and times (no text) of appended messages you had read or been reminded about that the user later retracted - ignore them. Cross-project reading is only permitted when the target workflow_id is explicitly provided.",
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
- "checkpoint_save": Save a structured checkpoint with objective, decisions, completed claims with evidence, next actions, blockers, and resume instructions, plus an optional basis (task_revision, report_sequence, feedback_sequence, git_commit). If basis.git_commit is omitted the server uses the workflow's latest delivery anchor (the 'commit' you passed to interactive_feedback) and links the capsule to that delivery on the Web timeline. Each save auto-increments the revision. Old checkpoints are marked as superseded.
- "checkpoint_get": Retrieve the latest checkpoint for a workflow. Set replay_after_checkpoint=true to also get recent deltas. Set max_bytes to limit response size.
- "checkpoint_list": List historical checkpoints for a workflow.
- "checkpoint_verify": Check if a checkpoint is still fresh (compares its age, the sessions created after it, and — when the basis has a git_commit — the workflow's latest delivery anchor against that commit). Returns freshness status and any drift reasons.

## Note Actions (AI Notes)
- "note_save": Create or update a note. Provide workflow_id + note_key for upsert (same key overwrites). Provide session_id to bind to a specific session. Notes are editable free-form text for recording thinking fragments, findings, and investigation trails.
- "note_get": Retrieve a note by note_id or by workflow_id + note_key.
- "note_list": List notes filtered by workflow_id and/or session_id.
- "note_update": Update note content by note_id.
- "note_delete": Delete a note by note_id.

## Discovery
- "list_workflows": List known workflows within the current project (strictly scoped by project_directory and host_name), including latest checkpoint info, session count, current phase, and last activity time. Provide project_directory to prevent seeing workflows from other projects.

## Phase Control
- "set_phase": Update the current workflow phase without triggering a full feedback interaction. Requires workflow_id and phase_id ('discuss' | 'plan' | 'dev'; legacy 'assess' / 'verify' / 'done' are mapped and the receipt carries a phase_alias note). The phase change is broadcast via SSE and injected into subsequent MCP response headers as current_phase + phase_prompt. The receipt also lists available_phases and warns when the requested phase is not in the workflow's phase list.

## Task List Actions (work items independent of sessions)
A shared task list the user sees on the Web "任务" page. Use it to keep multi-step work traceable across sessions and context resets; do not use it for micro-steps. These actions handle ONE task at a time — to see the whole board, define the module plan, or create / update several tasks, use the separate task_board tool (overview / list / plan / batch) instead of calling these repeatedly.
- "task_create": Requires workflow_id + title. Optional kind, stage, state, module, percent, labels, refs, description, progress, note, parent_task_id, assignee (management agent / user only), session_id. Returns the task with its task_id (the 12 in #12). module must be an existing module path (see task_board). Always write a description (one short paragraph: what the task is and what counts as done) — the title alone does not tell the user what to check.
- "task_update": Requires workflow_id + task_id. Any of state, stage, progress, percent, conclusion, description, module, labels, refs, title, kind, parent_task_id, note, assignee, claim. progress OVERWRITES the latest-progress summary (max 500 chars) and the change is appended to the timeline (last 20 kept). Editing description / module is housekeeping, not progress: no timeline entry and the last-progress time stays. Pass session_id so the task is linked to the session you are working in (many-to-many).
- "task_get": Requires workflow_id + task_id. Returns the task (full description) with its timeline and linked sessions.
- "task_list": Requires workflow_id. Defaults to the open tasks (todo / doing / review), most active first; use states / include_closed / kind / query / assignee / limit to change that.
Shared board: workflows of one project share ONE task board and its #numbers. A management agent (named by the user) or the user can hand a task to another workflow (assignee); the assignee answers with claim=accept or claim=decline (note = reason), works as usual, then claim=deliver (state -> review, with a delivery note). An assigned task is not finished by its assignee: the assigner / a management agent / the user accepts (claim=accept_delivery, state -> done, conclusion required) or sends it back (claim=reject_delivery, note = reason). Ordinary agents only write their own tasks (created by them or assigned to them) and read the rest. Full details are in the task_board tool description.
Rules: the state is bound to your own development flow — review means 'verifying' (regression, relevant e2e, deploy smoke), not 'waiting for the user'. Once the work is verified set state=done yourself and write the 1-3 line conclusion (what was done, how it was verified) in the same call; the server rejects done without a conclusion. Do not create a task for something that is finished in this very turn (a new task cannot be created as done). Module paths follow the project's stable functional areas (see "Choosing modules" in task_board) — never conversation rounds, dates or work phases; call task_board overview first to see the existing paths.

## Inbox Actions (messages the user appended while you were working)
The user can append messages in the Web UI at any time, even while you are busy; they wait in an inbox instead of interrupting you. Each message has its own state: queued -> delivered (you read it) -> acked (you confirmed it). The next interactive_feedback call also picks up everything still queued as an instant reply, as before.
- "inbox": Requires workflow_id. Returns the full text (and images) of the messages you have not read yet, marks them delivered, and also lists the ones you read earlier but have not acknowledged. If the user retracted a message you had already read — or one you had only been reminded about (hook / receipt line) and not read yet — it tells you once: ignore that message (for one you had not read, only its number and age are given, no text). recent=true also lists what you acknowledged during the last day. When nothing is waiting it returns a one-line "no new messages" and costs almost nothing, so calling it between long stretches of work is fine.
- "inbox_ack": Requires workflow_id. Confirms messages you have handled (ids to pick some, omit for all you have read). Optional — your next interactive_feedback report acknowledges them too.
- "inbox_peek": Read-only status (counts, preview, age). Meant for editor hooks, not for the agent.
When any tool receipt for a workflow ends with a line starting with "inbox:", unread appended messages are waiting: call "inbox" first, treat what you read as the user's new instructions (same trust as normal feedback), then carry on. The preview in that line is only the beginning of the latest message — read the full text before acting.

## Workflow Sheet Actions (Unified Session Markdown Document)
- "session_doc_save": Save or update the workflow's human-readable session document (Workflow Sheet). Requires workflow_id and content (Markdown). Stored persistently with note_key="session_doc" and viewable in the Web UI drawer.
- "session_doc_get": Retrieve the workflow's session document (Workflow Sheet). Requires workflow_id.

## Key Principles
- Checkpoints are append-only (never overwrite history); original records are preserved.
- Notes do NOT replace original session history — they are supplementary thinking memos.
- Pass 'commit' on interactive_feedback so checkpoints inherit a git anchor automatically and freshness can be verified.
- Save checkpoints at semantic boundaries: after key decisions, stage completions, before pauses, and before completion reports.`,
			InputSchema: workflowContextSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "task_board",
			Description: `Shared project task board — what is being built, how far along each piece is, and what the user watches live on the Web "任务" page (a button at the top of every session). It works on an EMPTY board: when nothing exists yet, action="overview" returns a step-by-step how-to instead of an empty list, so call it first whenever you are unsure.

Use it when the work has about 3 or more steps, spans sessions or context resets, or the user wants something tracked. Skip it for one-off micro-steps. After a context reset, call "overview" to see what is doing or waiting for review.

Model: a per-workflow plan tree (module -> feature, e.g. "终端" -> "终端/历史日志") plus tasks (#12) filed under a module path. Module names are a CLOSED list so labels never drift: a task can only use an existing path; define new ones with "plan" first (the error message lists the existing paths).

Choosing modules (first use, board still empty): a module is a STABLE functional area of the project (a subsystem, product area or long-running workstream) and a feature is a concrete capability inside it. Derive them from the project itself (directory layout, README, design docs): usually 2-5 domain modules plus 2-3 cross-cutting workstreams such as release/ops, quality/testing, docs/backlog. Do NOT group by conversation round or batch ("第N轮", "第N批"), date, session, person, or work phase (assess / plan / dev / test), and do not create catch-all buckets such as "其他" / "杂项" / "misc" — those labels stop meaning anything within weeks and turn the board into a log.

Keeping it tidy (later use): call "overview" before filing — it returns a "maintain" rule list and "warnings" when the structure drifts; act on them. File each new task under an EXISTING module/feature path; add a feature with "plan" only when nothing fits (a new top-level module should be rare, and never one per conversation). One deliverable = one task that moves through state / stage / progress — not one task per phase, and not a parent "round" task that bundles whatever the user asked this time (parent_task_id is for a genuinely multi-part deliverable). When the user asks for several things at once, file each under its own functional area. Keep "未归类" empty: move strays with batch {task_ids, module}.

Collaboration on a shared project board: the workflows of one project share this board and its #numbers. A management agent (named by the user in the project page) or the user hands a task to another workflow with assignee=<workflow_id> (batch op; '' withdraws it). The assignee finds it under "assigned_to_you" in overview (or list assignee=me) and answers with claim=accept (todo -> doing) or claim=decline with note = the reason. It works as usual (progress / percent / note) and hands the result over with claim=deliver (state -> review; note / progress / conclusion say what was done and how it was verified). An assigned task is NOT finished by its assignee: the assigner (or a management agent / the user) accepts it with claim=accept_delivery (state -> done, conclusion required) or sends it back with claim=reject_delivery (note = the reason); overview lists these under "awaiting_your_acceptance". Setting state=doing / review yourself on a task assigned to you counts as accept / deliver. Ordinary agents may only write their own tasks (created by them or assigned to them) and read the rest. When you delegate, hand over at least 2 tasks per call — split the work into the smallest independent pieces; hand over fewer only when there really are fewer.

Actions:
- "overview" (default): counts by state, the module tree with open / done / total / percent, the tasks that are doing or in review, a few todo, tasks assigned to you / waiting for your acceptance (shared boards), and a how-to when the board is empty.
- "list": module (path; includes its features; '未归类' = no module), states (default todo / doing / review), include_closed, kind, query, assignee ('me' / 'none' / a workflow_id), sort ('plan' = module-tree order, 'recent' = newest progress first), limit (default 30, max 100).
- "plan": declare the module tree in ONE call — modules=[{name, description?, features:[{name, description?}], rename?, remove?}]. Idempotent (re-sending the same plan changes nothing) and atomic (one invalid node -> nothing is applied). New nodes are appended; reorder=true applies the order you sent. remove only works on a node with no tasks and no features. Keep it small: 3-8 modules, at most 30 features each, 2 levels.
- "batch": create and/or update many tasks in ONE call — ops=[...] (max 30). An op without task_id creates a task (title required; an open task with the same title in the same module is reused instead of duplicated). An op with task_id — or task_ids, to apply the same change to several tasks — updates them (state, stage, progress, percent, conclusion, description, module, labels, refs, note, title, kind, parent_task_id, assignee, claim). Give every created task a description (what it is, what counts as done). Results are per task and one failing op never blocks the others. Pass modules as well and the plan is applied first, so "plan + file everything" is a single call.

Field rules: state goes todo -> doing -> review -> done (shelved is the side branch); it is bound to your own development flow: review = verifying, done = verified and delivered, set by you together with a conclusion (the server rejects done without one). progress OVERWRITES the previous one (max 500 characters): say where it stands now and what is next, not a changelog. percent is 0-100, only when you have a believable number (-1 clears it; otherwise the bar is derived from state / sub-tasks). stage (assess -> plan -> dev -> improve) says what kind of work the task is in right now; it is independent of state and kind, the Web task page shows it as a second progress bar, and it is fine to leave it out. Write a 1-3 line conclusion when a task reaches done or shelved. Pass session_id to link tasks to the session you work in. Re-filing a task under another module does not change its last-progress time.`,
			InputSchema: taskBoardSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "project_docs",
			Description: `Project documentation library — durable Markdown docs per workflow (design, ADRs, runbooks, specs). Paths form a tree; each doc has revisions (optimistic concurrency via expected_rev). Use alongside task_board: tasks track progress; docs hold the reasoning and specifications.

When to use: starting or resuming work on a project (overview first); writing or updating design/decision/runbook content; searching prior decisions; exporting everything to local files for the user's repo (export); bootstrapping from an old codebase (import); copying the Workflow Sheet into the library (import_sheet).

Actions:
- overview (default): counts, path tree, recent updates, blueprint gap count, usage hint (empty library gets a starter guide).
- list: filter by query, kind, status, tag, prefix, since. Returns metadata only (id, seq, path, title, kind, status, tags, rev, hash, timestamps) — never the body: read content with get, or in bulk with export.
- get: by path or id; optional section; max_chars + next_offset for long bodies; rev for history.
- search: ranked snippets with anchors.
- save: create or update by path — full body, or append/prepend, or section + section_body; expected_rev on update; warnings (not errors) for invalid decision fences.
- history: revision list; delete archives (does not hard-delete).
- export: files[{path, rev, hash, content}] for model-side write to disk; known{path:hash} for delta; max_bytes + cursor pagination; includes generated INDEX.md.
- import: files[] with mode create|upsert, dry_run for counts.
- blueprint: recommended doc structure vs what exists; with_templates for skeleton Markdown.
- import_sheet: copy session Workflow Sheet to session/workflow-sheet.md (sessions/<workflow_id>/workflow-sheet.md when the workflow is in a project).
- grant / revoke / grants: delegate read | comment | edit on a scope YOU manage (a module you own, a doc you own, your own workflow) to another agent identity; revoke by grant id; grants lists the ones you gave or received. The grantee is told in its inbox. You cannot delegate manage. These work when the project is in audit or enforce mode and always need your identity (project_directory + host_name).
- data_get / data_set / data_list / data_delete {path|id, key, value, expected_version}: the JSON key-value data of an Artifact (a doc saved with render="html" or "data"; ≤200 keys, ≤256 KB total). The HTML page reads and writes the same data through window.relaymesh.data, so you and the user (or several agents) can share a board. Writes are audited (key + version only, never the value). Use expected_version when more than one writer may touch a key. HTML pages run in a sandbox (scripts only: no network, no cookies, no parent DOM) and talk to the host only through window.relaymesh: await data.get(key) -> {key,value,version}; data.set(key,value,expectedVersion?) -> {key,version}; data.list(); data.remove(key); comment(text,anchor); window event 'relaymesh:data' (detail.key) fires when someone else changes data (your own writes do not echo back); height auto-resizes. Data writes are audited, not stored as doc revisions.
- comments {path|id, include_resolved?}: comments the user left on a doc / Artifact (anchor = where); comment {path|id, body, anchor?}: leave one; comment_resolve {path|id, comment_id, resolved?}: mark handled after you addressed it. New user comments also arrive in your inbox.
- links {path|id}: outlinks (documents this one points to via [[path]] or relative .md links, with whether the target exists) and backlinks (documents that point to this one). Use it before renaming or deleting a doc.
- module (scope=project): one module page — owner, overview doc, design doc, decisions, artifacts, recent revisions, related tasks.

Project knowledge base (scope=project): the user groups directories into a project; the docs of all its workflows then form ONE shared library with a module tree (path modules/<module>/...). Calls without scope behave exactly as before. In scope=project, save by path updates the existing project doc (even one written by another workflow) — use expected_rev; paths are unique across the project. Project-wide limit 2000 docs.

Limits: 500 docs per workflow; body ≤ 200 KB; ≤ 8 tags × 24 chars; import batch ≤ 100 files / 2 MB.`,
			InputSchema: projectDocsSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "async_ask",
			Description: `Non-blocking confirmation items ("待确认", referenced as Q12): ask the user something and keep working.

Use it when the decision is reversible (risk L0/L1) and you can continue with the rollback-safe parts meanwhile (default_option is your working assumption, NOT the user's answer). For kind=plan bind the task with task_id and put the plan in body; do the irreversible parts only after the approval arrives.
For anything irreversible or high-risk (deploy, push to a remote, delete data, change credentials, irreversible migration, publish) use risk=L2. In async mode (the receipt says "mode: async") that card is what you send instead of stopping to wait: keep working on whatever does not depend on it, and do NOT execute the step until the user explicitly approves it - no answer means not approved. An L2 item carries no default_option (the server rejects one), is never part of a batch or a one-click "adopt recommendations", and never expires by itself; only the user can answer it. If it is still unapproved when you wrap up, put the dependent tasks on hold (shelved) and name the item (Q12) in the note. In sync mode keep using the blocking interactive_feedback for these.

Actions:
- create (default): returns at once with ref (Q12), pending_count and how to read the answer. Identical title+options while still pending are deduplicated; at most 30 new items per hour per workflow — merge related questions into one item.
- get {ref}: one item with its status and answer.
- list {status?}: pending by default; status=all for everything.
- withdraw {ref}: retract your own pending item.

The answer arrives as an inbox message ("确认项答复"): read it with workflow_context(action:"inbox") when a receipt shows an "inbox:" line or the editor hook reminds you, or poll get / list. An answer never unblocks an interactive_feedback that is already waiting.

Custom answers: every item lets the user write a custom answer of their own when none of your options fit (answer.custom in get / list). For a single-choice item it replaces the option choice (answer.options is then empty and the inbox line says "没有选预设选项…自定义答复：…"); for a multi-choice item it may come together with chosen options. It is always the last segment of the line. Never assume an item can only be answered with one of your options. For kind=plan or risk=L2 a custom answer is NOT an approval: treat it as a change request and stay read-only (do not execute) until a real approval arrives.`,
			InputSchema: asyncAskSchema,
		})
		tools = append(tools, ToolDefinition{
			Name: "chat",
			Description: `Your side of the conversation with the user, for workflows where async chat is ON (the same switch as interactive_feedback wait:false). On workflows where it is off the tool answers "not_chat_mode" — keep using interactive_feedback there, and never turn the mode on yourself.

Actions:
- read (default): the messages from the user you have not read yet (their messages, answers to your async_ask cards, system lines); moves your read position. With before / after / q it pages through the history of both sides instead and does not move your position.
- send {text, title?, phase?}: post a message. It never blocks. If you need the user to decide before you can continue, use interactive_feedback instead.
- list: your conversations (currently just your own 1:1 chat).
- members: who is in the conversation.
- status: how many messages from the user are waiting for you.
- end {reason?, summary?}: say you are done / blocked / aborted / handing off. The user sees a system line and your state is shown as ended.

Replies to a specific message of yours arrive with a leading marker like 【回复 9f02517c】; reply_hash on a message is that hash.`,
			InputSchema: chatSchema,
		})
	}

	tools = append(tools, usageGuideToolDefinitions(perms)...)
	tools = append(tools, terminalToolDefinitions(perms)...)
	tools = append(tools, presetRunToolDefinitions(perms)...)

	return tools
}
