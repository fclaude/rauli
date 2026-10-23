# Security

If you find a security problem in Raulí, please tell me privately before you tell anyone else. GitHub has a way to do that: open the Security tab of this repository and choose Report a vulnerability. I'll answer as soon as I can, and I'd like to fix the problem before it's described in public.

Raulí exists to let a program use a browser under rules and with a keychain, so the problems I most want to hear about are the ones that break those promises. A way past the rules, such as an address or an action that should have been refused. A secret that shows up where it shouldn't: in a result, a snapshot, a log or the audit file. A way to open the live view without its address, or to act through it without taking control. A browser that stays running after Raulí has gone.

There's no release yet, so only `main` gets fixes.
