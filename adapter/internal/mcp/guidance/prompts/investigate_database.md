---
name: investigate_database
title: Investigate a slow or failing database
description: Look at a database's status, metrics, usage, history and alerts to find out what is wrong, then open a support ticket if it needs Percona.
access: read
arguments:
  - name: database
    description: The database id, copied exactly from list_databases.
    required: true
---
Find out what is wrong with the database {{.database}}. Look before you conclude, quote what the tools return, and never invent a number.

1. Call get_database. Note its status and its message.
2. Call get_metrics for the last hour and then the last day, to see whether CPU, memory or storage changed. When a tool says monitoring is not connected, say so and use get_database_usage instead.
3. Call get_database_history to see what happened to it recently: changes, backups, restores and events.
4. Call get_database_alerts to see what is firing.
5. Summarize what you found: what is wrong, since when, and the most likely cause. Say which of these you could not check.
{{- if .Changes}}
6. If the cause is outside the user's control, or you cannot find it, offer to open a support ticket. Only after the user agrees, call create_support_ticket with a subject that says what stopped working, a description of what you saw and tried, and databaseId set to {{.database}}. Give the user the ticket number and the portal link.
{{- else}}
6. If the cause is outside the user's control, or you cannot find it, tell the user to open a support ticket. Opening one is not allowed in this session, because it needs change access.
{{- end}}
