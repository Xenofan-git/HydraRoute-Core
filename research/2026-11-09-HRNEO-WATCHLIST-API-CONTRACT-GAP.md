# Superseded: Watchlist API contract recovery gap

The earlier note that the original Watchlist contract was not recovered is no longer current.

The original contract was recovered from Ground-Zerro/HydraRoute commit 7fb556ec493adda131f3e8f97106226751b59528, especially Neo/docs/WATCHLIST_API.md, Neo/docs/WATCHLIST_LIMITS.md, Neo/source/src/watchlist_api.c, Neo/source/src/watchlist.c and Neo/source/tests/check_wlapi.c.

See research/2026-11-09-HRNEO-WATCHLIST-API-CONTRACT.md for the exact Unix socket protocol, MATCH/DUMP response formats, lowest-rank-wins semantics, lifecycle, limits and Core/Debian adaptation.

Contract recovery and Core source implementation are complete. GitHub Actions #191/#192 passed; runtime deployment remains pending explicit approval.
