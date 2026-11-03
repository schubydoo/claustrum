package main

// Each params struct has an ExpandPaths method, so it implements
// rpc.PathExpander, the parameter type of rpc.Bind.
func (p *spawnParams) ExpandPaths() { p.Cwd = expandPath(p.Cwd) }

// The process-control params address a process by id and carry no filesystem
// paths, so expansion is a no-op. Declared explicitly rather than omitted: the
// interface is what forces that judgement to be made for every params type.
func (p *stdinParams) ExpandPaths()       {}
func (p *killParams) ExpandPaths()        {}
func (p *killAndWaitParams) ExpandPaths() {}
func (p *reattachParams) ExpandPaths()    {}
