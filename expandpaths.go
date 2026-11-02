package main

// Each params struct has an ExpandPaths method, so it implements
// rpc.PathExpander, the parameter type of rpc.Bind.
func (p *pathParams) ExpandPaths()     { p.Path = expandPath(p.Path) }
func (p *fileReadParams) ExpandPaths() { p.Path = expandPath(p.Path) }
func (p *extractTarParams) ExpandPaths() {
	p.ArchivePath = expandPath(p.ArchivePath)
	p.DestDir = expandPath(p.DestDir)
}
func (p *gitParams) ExpandPaths() {
	p.Path = expandPath(p.Path)
	p.BaseRepo = expandPath(p.BaseRepo)
	p.WorktreePath = expandPath(p.WorktreePath)
	p.WorktreeRoot = expandPath(p.WorktreeRoot)
	// BranchName and SourceBranch are refs, not paths — never expanded.
}
func (p *spawnParams) ExpandPaths() { p.Cwd = expandPath(p.Cwd) }

// The process-control params address a process by id and carry no filesystem
// paths, so expansion is a no-op. Declared explicitly rather than omitted: the
// interface is what forces that judgement to be made for every params type.
func (p *stdinParams) ExpandPaths()       {}
func (p *killParams) ExpandPaths()        {}
func (p *killAndWaitParams) ExpandPaths() {}
func (p *reattachParams) ExpandPaths()    {}
