export namespace config {
	
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

export namespace sftp {
	
	export class Item {
	    Name: string;
	    Size: number;
	    IsDir: boolean;
	    Mode: string;
	    ModTime: string;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Name = source["Name"];
	        this.Size = source["Size"];
	        this.IsDir = source["IsDir"];
	        this.Mode = source["Mode"];
	        this.ModTime = source["ModTime"];
	    }
	}

}

