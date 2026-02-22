export namespace main {
	
	export class Defaults {
	    application_name: string;
	    application_version: string;
	    destination: string;
	    manifest_url: string;
	    pathmap_url: string;
	    timeout_seconds: number;
	
	    static createFrom(source: any = {}) {
	        return new Defaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.application_name = source["application_name"];
	        this.application_version = source["application_version"];
	        this.destination = source["destination"];
	        this.manifest_url = source["manifest_url"];
	        this.pathmap_url = source["pathmap_url"];
	        this.timeout_seconds = source["timeout_seconds"];
	    }
	}
	export class PrereqStatus {
	    ok: boolean;
	    missing: string[];
	    target_dir: string;
	    working_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new PrereqStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.missing = source["missing"];
	        this.target_dir = source["target_dir"];
	        this.working_dir = source["working_dir"];
	    }
	}

}

