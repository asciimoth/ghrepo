Output demo.gif

Set Shell "bash"
Set FontFamily "Fira Code"
Set FontSize 13
Set LetterSpacing 0
Set Width 1040
Set Height 610
Set TypingSpeed 20ms
Set LoopOffset 69%

Env PS1 "$ "
Env UNICODE_PROGRESS_BAR "true"

# Build a demo CLI with slow local fixtures and a disposable Git remote.
Hide
Type "source scripts/demo-setup.sh"
Enter
Wait+Line /[$]/
Type "clear"
Enter
Show

Type "zpg ghrepo init --repo asciimoth/repo --site https://repo.moth.contact"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo add --comment 'Example CLI' --tags cli,tools https://github.com/asciimoth/demo/releases/download/v1/hello_1.2.0-1_amd64.deb"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo add --comment 'Example RPM' --tags cli,tools https://github.com/asciimoth/demo/releases/download/v1/empty-0.1-1.x86_64.rpm"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo rebuild"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo check --refresh-sources"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo add --comment 'Example CLI' --tags cli,tools https://github.com/asciimoth/demo/releases/download/v2/hello_2.0.0-1_amd64.deb"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo publish --dry-run"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "zpg ghrepo publish --message 'chore(repo): publish packages'"
Enter
Wait+Line /[$]/
Sleep 700ms

Type "git log -1 --oneline && git status --short"
Enter
Wait+Line /[$]/
Sleep 2s
