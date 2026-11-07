package main

// hostTermSiblingSparesLegacyOnly is false on Linux: a sibling run dir of the
// host-term shape still spares both plugin roots there. No Linux row had such a
// sibling, so Linux keeps the rule that it had (claustrum's own, not measured).
const hostTermSiblingSparesLegacyOnly = false
