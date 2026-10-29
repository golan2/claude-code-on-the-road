# Archive and unarchive sessions

`_archived` sits directly under the root folder and holds finished sessions. GoApp never scans it, so nothing inside is picked up or answered.

**Automatic.** Every time GoApp starts, it moves session folders with no request, response or pid file written for `archiveAfterDays` days into `_archived`. Read the number with a config request. If the key is absent, automatic archiving is off. An old unanswered request does not stop it.

**One session by hand.** Move the folder into `_archived` with a plain Drive move.

**Unarchive**
1. Get the root path: config request, key `"watchFolder"`.
2. List `<watchFolder>/_archived` with an exec request (`ls`). To identify a session, read its request files or `__session_conf.json` with exec.
3. Move it back to the root with exec (`mv`). Never overwrite a folder that already has that name. Tell Izik instead.
4. Send your next request into it right away. Moved files keep their old dates, so with no new request GoApp archives it again at its next start. A new request is fresh activity and keeps it.
