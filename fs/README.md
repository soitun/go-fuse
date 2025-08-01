
Objective
=========

A high-performance FUSE API that minimizes pitfalls with writing
correct filesystems.

Decisions
=========

   * Nodes contain references to their children. This is useful
     because most filesystems will need to construct tree-like
     structures.

   * Nodes contain references to their parents. As a result, we can
     derive the path for each Inode, and there is no need for a
     separate PathFS.

   * Nodes can be "persistent", meaning their lifetime is not under
     control of the kernel. This is useful for constructing FS trees
     in advance, rather than driven by LOOKUP.

   * The NodeID (used for communicating with the kernel, not to be
     confused with the inode number reported by `ls -i`) is generated
     internally and immutable for an Inode.  This avoids any races
     between LOOKUP, NOTIFY and FORGET.
     
   * The mode of an Inode is defined on creation.  Files cannot change
     type during their lifetime. This also prevents the common error
     of forgetting to return the filetype in Lookup/GetAttr.
     
   * No global treelock, to ensure scalability.

   * Support for hard links. libfuse doesn't support this in the
     high-level API.  Extra care for race conditions is needed when
     looking up the same file through different paths.

   * do not issue Notify{Entry,Delete} as part of
     AddChild/RmChild/MvChild: because NodeIDs are unique and
     immutable, there is no confusion about which nodes are
     invalidated, and the notification doesn't have to happen under
     lock.

   * Directory reading uses the FileHandles as well, the API for read
     is one DirEntry at a time. FileHandles may implement seeking, and we
     call the Seek if we see Offsets change in the incoming request.
   
   * Method names are based on syscall names. Where there is no
     syscall (eg. "open directory"), we bias towards writing
     everything together (Opendir)
