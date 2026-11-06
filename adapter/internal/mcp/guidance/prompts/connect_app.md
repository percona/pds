---
name: connect_app
title: Connect an app to a database
description: Check that a database is ready and reachable from where the app runs, then give the connection details without the password.
access: read
arguments:
  - name: database
    description: The database id, copied exactly from list_databases.
    required: true
---
Help the user connect their app to the database {{.database}}.

1. Call get_database for {{.database}}. If its status is not ready or its connection is missing, call wait_for_database with until ready, and tell the user what its message says if something blocks the database.
2. Ask where the app will run. If it runs in the same VPC as the environment, a private database is reachable. If it runs on a laptop or another host, the database must be public, and the address that machine connects from must be among its allowed ranges, written as a /32. Ask the user for that address and compare it with the database's access in get_database. Never advise 0.0.0.0/0.
3. If the access does not fit where the app runs, say so. No tool changes access after a database exists, so a database with the right access has to be created{{if .Changes}}: offer to create one{{end}}.
4. Give the user the host, port and username from get_database, and the database name if the app needs one. The password is not available to you. The user copies it from the console with Copy password and puts it in the app's own settings. It must not appear in the chat, in a tool argument or in a command.
5. Offer to write the connection settings into the app's configuration with the password left for the user to fill in, and remind them not to commit a file that holds it.
