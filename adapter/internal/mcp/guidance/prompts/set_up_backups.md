---
name: set_up_backups
title: Set up backups
description: Choose a backup schedule and how many copies to keep, then run a first backup and wait for it.
access: change
arguments:
  - name: database
    description: The database id, copied exactly from list_databases.
    required: true
---
Set up backups for the database {{.database}}.

1. Call get_database and note its current backup schedule. Call list_backups to see the backups it has.
2. Ask the user how much data they can afford to lose, and how long they keep copies. Suggest a daily schedule at a quiet hour, kept for 7 copies, unless they need more. All times are UTC.
3. Show the user the schedule and the number of copies to keep, and ask them to confirm. Then call set_backup_schedule with enabled true, a period, an hour for a daily or weekly period, a weekday for a weekly one, and retentionCopies.
4. Call get_database to check that the schedule was written. It can take a short while to appear on the environment.
5. Offer a first backup now. Only after the user agrees, call backup_database, then wait_for_backup with the returned backup id. When it ends in progress, call it again: a large backup can take many minutes.
6. Tell the user the result. If a tool answers that backups are not available, tell them that backups are not set up for the environment.
