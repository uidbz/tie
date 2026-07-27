# Copyright 2023-2026 Johan Straarup
# Distributed under the terms of the BSD 3-Clause License

EAPI=8

inherit acct-group

DESCRIPTION="Group for the tie triple-store and filehost services"

# -1 lets Portage allocate a free GID dynamically. Pin a fixed number here if
# you need the GID to match across several machines.
ACCT_GROUP_ID=-1
