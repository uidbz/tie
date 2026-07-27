# Copyright 2023-2026 Johan Straarup
# Distributed under the terms of the BSD 3-Clause License

EAPI=8

inherit acct-user

DESCRIPTION="User for the tie triple-store and filehost services"

# -1 lets Portage allocate a free UID dynamically. Pin a fixed number here if
# you need the UID to match across several machines.
ACCT_USER_ID=-1
ACCT_USER_GROUPS=( tie )
ACCT_USER_HOME=/var/lib/tie
ACCT_USER_SHELL=/sbin/nologin
