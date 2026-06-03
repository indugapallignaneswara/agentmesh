---
description: Send a direct message to a named AgentMesh teammate. Usage: /ask <agent> <message...>
argument-hint: "<agent> <message...>"
allowed-tools: Bash(coord *)
---

You used `/ask` to address an AgentMesh teammate.

- Recipient: **$1**
- Message: **$ARGUMENTS**

The message has been delivered to `$1`'s inbox:

!`coord send --to "$1" --body "$(echo "$ARGUMENTS" | cut -d' ' -f2-)"`

The recipient will see it at their next turn boundary (their session pulls the
inbox via the AgentMesh hook). If the command above printed an error, tell the
user what failed (e.g. the recipient hasn't joined the workspace, or
AGENTMESH_* env vars aren't set) and suggest a fix. Otherwise confirm the
message was sent to **$1**.
