"""
Feature modules. Every .py file (or package) in this folder that exposes a
`router` is loaded automatically by main.py. Files starting with "_" are ignored.

Conventions for a module:
  * The first line of the module docstring is its short description. It is shown
    in /modules and /help, so keep it to a few words, e.g. Core bot commands.
  * HELP is a list of (usage, description[, minimum_role]) entries. It feeds the
    /help panel (see core/help_index.py). Modules without HELP are not listed there.
  * PROTECTED = True makes the module impossible to disable (only for core
    bot-management modules such as root).
"""
