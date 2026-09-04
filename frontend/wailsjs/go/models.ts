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

}

