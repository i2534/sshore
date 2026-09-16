export namespace config {
	
	export class AppSettings {
	    auto_reconnect_default: boolean;
	    theme: string;
	    font_scale: number;
	    latin_font?: string;
	    cjk_font?: string;
	    auto_start_on_launch: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.auto_reconnect_default = source["auto_reconnect_default"];
	        this.theme = source["theme"];
	        this.font_scale = source["font_scale"];
	        this.latin_font = source["latin_font"];
	        this.cjk_font = source["cjk_font"];
	        this.auto_start_on_launch = source["auto_start_on_launch"];
	    }
	}
	export class Bookmark {
	    name: string;
	    scope: string;
	    host: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new Bookmark(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.scope = source["scope"];
	        this.host = source["host"];
	        this.path = source["path"];
	    }
	}
	export class Host {
	    alias: string;
	    host_name: string;
	    user: string;
	    port: number;
	    identity_file: string;
	    proxy_jump: string;
	
	    static createFrom(source: any = {}) {
	        return new Host(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.alias = source["alias"];
	        this.host_name = source["host_name"];
	        this.user = source["user"];
	        this.port = source["port"];
	        this.identity_file = source["identity_file"];
	        this.proxy_jump = source["proxy_jump"];
	    }
	}
	export class RecentLocal {
	    path: string;
	    ts: string;
	
	    static createFrom(source: any = {}) {
	        return new RecentLocal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.ts = source["ts"];
	    }
	}
	export class RecentRemote {
	    host: string;
	    path: string;
	    ts: string;
	
	    static createFrom(source: any = {}) {
	        return new RecentRemote(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.path = source["path"];
	        this.ts = source["ts"];
	    }
	}
	export class RecentSFTP {
	    host: string;
	    remote_dir: string;
	    local_dir: string;
	    ts: string;
	
	    static createFrom(source: any = {}) {
	        return new RecentSFTP(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.remote_dir = source["remote_dir"];
	        this.local_dir = source["local_dir"];
	        this.ts = source["ts"];
	    }
	}
	export class SyncRule {
	    id: string;
	    name: string;
	    host: string;
	    user?: string;
	    kind: string;
	    remote_path: string;
	    local_path: string;
	    max_depth: number;
	    excludes: string[];
	    mirror_delete: boolean;
	    force_poll: boolean;
	    poll_interval_s: number;
	    auto_reconnect?: boolean;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SyncRule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.user = source["user"];
	        this.kind = source["kind"];
	        this.remote_path = source["remote_path"];
	        this.local_path = source["local_path"];
	        this.max_depth = source["max_depth"];
	        this.excludes = source["excludes"];
	        this.mirror_delete = source["mirror_delete"];
	        this.force_poll = source["force_poll"];
	        this.poll_interval_s = source["poll_interval_s"];
	        this.auto_reconnect = source["auto_reconnect"];
	        this.enabled = source["enabled"];
	    }
	}
	export class Tunnel {
	    id: string;
	    name: string;
	    mode: string;
	    host: string;
	    user?: string;
	    port?: number;
	    listen_bind: string;
	    listen_port: number;
	    target_host: string;
	    target_port: number;
	    proxy_jump?: string;
	    auto_reconnect: boolean;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Tunnel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mode = source["mode"];
	        this.host = source["host"];
	        this.user = source["user"];
	        this.port = source["port"];
	        this.listen_bind = source["listen_bind"];
	        this.listen_port = source["listen_port"];
	        this.target_host = source["target_host"];
	        this.target_port = source["target_port"];
	        this.proxy_jump = source["proxy_jump"];
	        this.auto_reconnect = source["auto_reconnect"];
	        this.enabled = source["enabled"];
	    }
	}

}

export namespace localfs {
	
	export class Hit {
	    path: string;
	    isDir: boolean;
	    size: number;
	    modTime: string;
	
	    static createFrom(source: any = {}) {
	        return new Hit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	    }
	}

}

export namespace main {
	
	export class AppInfo {
	    name: string;
	    version: string;
	    repo: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.repo = source["repo"];
	    }
	}
	export class LocalSearchOutcome {
	    hits: localfs.Hit[];
	    scanned: number;
	    unreadable: number;
	    truncated: boolean;
	    cancelled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LocalSearchOutcome(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hits = this.convertValues(source["hits"], localfs.Hit);
	        this.scanned = source["scanned"];
	        this.unreadable = source["unreadable"];
	        this.truncated = source["truncated"];
	        this.cancelled = source["cancelled"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalSearchRequest {
	    id: string;
	    root: string;
	    pattern: string;
	    maxDepth: number;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new LocalSearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.root = source["root"];
	        this.pattern = source["pattern"];
	        this.maxDepth = source["maxDepth"];
	        this.limit = source["limit"];
	    }
	}
	export class Locations {
	    bookmarks: config.Bookmark[];
	    localRecents: config.RecentLocal[];
	    remoteRecents: config.RecentRemote[];
	
	    static createFrom(source: any = {}) {
	        return new Locations(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bookmarks = this.convertValues(source["bookmarks"], config.Bookmark);
	        this.localRecents = this.convertValues(source["localRecents"], config.RecentLocal);
	        this.remoteRecents = this.convertValues(source["remoteRecents"], config.RecentRemote);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PathInfo {
	    path: string;
	    name: string;
	    isDir: boolean;
	    size: number;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new PathInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.err = source["err"];
	    }
	}
	export class RemoteSearchRequest {
	    id: string;
	    host: string;
	    root: string;
	    pattern: string;
	    maxDepth: number;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new RemoteSearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.host = source["host"];
	        this.root = source["root"];
	        this.pattern = source["pattern"];
	        this.maxDepth = source["maxDepth"];
	        this.limit = source["limit"];
	    }
	}

}

export namespace sftp {
	
	export class Item {
	    name: string;
	    size: number;
	    isDir: boolean;
	    mode: string;
	    modTime: string;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.size = source["size"];
	        this.isDir = source["isDir"];
	        this.mode = source["mode"];
	        this.modTime = source["modTime"];
	    }
	}
	export class SearchHit {
	    path: string;
	    isDir: boolean;
	    size: number;
	    modTime: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchHit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	    }
	}
	export class SearchOutcome {
	    hits: SearchHit[];
	    scanned: number;
	    unreadable: number;
	    truncated: boolean;
	    cancelled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SearchOutcome(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hits = this.convertValues(source["hits"], SearchHit);
	        this.scanned = source["scanned"];
	        this.unreadable = source["unreadable"];
	        this.truncated = source["truncated"];
	        this.cancelled = source["cancelled"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace sync {
	
	export class Conflict {
	    rel_path: string;
	    remote_size: number;
	    remote_mtime: string;
	    local_size: number;
	    local_mtime: string;
	    detected_at: string;
	
	    static createFrom(source: any = {}) {
	        return new Conflict(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rel_path = source["rel_path"];
	        this.remote_size = source["remote_size"];
	        this.remote_mtime = source["remote_mtime"];
	        this.local_size = source["local_size"];
	        this.local_mtime = source["local_mtime"];
	        this.detected_at = source["detected_at"];
	    }
	}

}

