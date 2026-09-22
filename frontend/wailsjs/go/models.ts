export namespace app {
	
	export class CheckUpdateResult {
	    available: boolean;
	    currentVersion: string;
	    version: string;
	    url: string;
	    size: number;
	    notes: string;
	    releaseUrl: string;
	    canInstall: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CheckUpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.currentVersion = source["currentVersion"];
	        this.version = source["version"];
	        this.url = source["url"];
	        this.size = source["size"];
	        this.notes = source["notes"];
	        this.releaseUrl = source["releaseUrl"];
	        this.canInstall = source["canInstall"];
	    }
	}
	export class MigrationPreview {
	    files: number;
	    bytes: number;
	    conflicts: string[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new MigrationPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.files = source["files"];
	        this.bytes = source["bytes"];
	        this.conflicts = source["conflicts"];
	        this.warnings = source["warnings"];
	    }
	}
	export class MigrationVerification {
	    id: string;
	    status: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new MigrationVerification(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	    }
	}
	export class PreviewWindowAccount {
	    user_id: string;
	    drive_id: string;
	
	    static createFrom(source: any = {}) {
	        return new PreviewWindowAccount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	    }
	}
	export class PreviewWindowSeed {
	    account: PreviewWindowAccount;
	    file: model.File;
	    files: model.File[];
	    capabilities: Record<string, any>;
	    kind: string;
	    preferences: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new PreviewWindowSeed(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account = this.convertValues(source["account"], PreviewWindowAccount);
	        this.file = this.convertValues(source["file"], model.File);
	        this.files = this.convertValues(source["files"], model.File);
	        this.capabilities = source["capabilities"];
	        this.kind = source["kind"];
	        this.preferences = source["preferences"];
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
	export class ProviderInfo {
	    ID: string;
	    Meta: drive.Meta;
	    Capabilities: drive.Capabilities;
	    Login: drive.LoginConfig;
	
	    static createFrom(source: any = {}) {
	        return new ProviderInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Meta = this.convertValues(source["Meta"], drive.Meta);
	        this.Capabilities = this.convertValues(source["Capabilities"], drive.Capabilities);
	        this.Login = this.convertValues(source["Login"], drive.LoginConfig);
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
	export class UpdateStatus {
	    revision: number;
	    phase: string;
	    info?: CheckUpdateResult;
	    downloaded: number;
	    total: number;
	    path: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.phase = source["phase"];
	        this.info = this.convertValues(source["info"], CheckUpdateResult);
	        this.downloaded = source["downloaded"];
	        this.total = source["total"];
	        this.path = source["path"];
	        this.error = source["error"];
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

export namespace drive {
	
	export class Capabilities {
	    provider: string;
	    mountedStorage: boolean;
	    download: boolean;
	    cloudVideoPreview: boolean;
	    offlineDownload: boolean;
	    search: boolean;
	    upload: boolean;
	    uploadMode: string;
	    createFolder: boolean;
	    createDateFolder: boolean;
	    createTextFile: boolean;
	    rename: boolean;
	    move: boolean;
	    copy: boolean;
	    recycleBin: boolean;
	    permanentDelete: boolean;
	    trashView: boolean;
	    trashRestore: boolean;
	    trashPurge: boolean;
	    trashClear: boolean;
	    createShare: boolean;
	    shareExpiration: boolean;
	    shareExpirationOptions?: number[];
	    sharePassword: boolean;
	    combinedShare: boolean;
	    importShare: boolean;
	    manageCreatedShares: boolean;
	    editCreatedShares: boolean;
	    cancelCreatedShares: boolean;
	    manageImportedShares: boolean;
	    shareHistory: boolean;
	    quickTransfer: boolean;
	    favorite: boolean;
	    encryption: boolean;
	    playbackHistory: boolean;
	    copyTree: boolean;
	    photoAlbum: boolean;
	    provideHashes: string[];
	    rapidUploadHashes: string[];
	    uploadConflictPolicies: string[];
	
	    static createFrom(source: any = {}) {
	        return new Capabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.mountedStorage = source["mountedStorage"];
	        this.download = source["download"];
	        this.cloudVideoPreview = source["cloudVideoPreview"];
	        this.offlineDownload = source["offlineDownload"];
	        this.search = source["search"];
	        this.upload = source["upload"];
	        this.uploadMode = source["uploadMode"];
	        this.createFolder = source["createFolder"];
	        this.createDateFolder = source["createDateFolder"];
	        this.createTextFile = source["createTextFile"];
	        this.rename = source["rename"];
	        this.move = source["move"];
	        this.copy = source["copy"];
	        this.recycleBin = source["recycleBin"];
	        this.permanentDelete = source["permanentDelete"];
	        this.trashView = source["trashView"];
	        this.trashRestore = source["trashRestore"];
	        this.trashPurge = source["trashPurge"];
	        this.trashClear = source["trashClear"];
	        this.createShare = source["createShare"];
	        this.shareExpiration = source["shareExpiration"];
	        this.shareExpirationOptions = source["shareExpirationOptions"];
	        this.sharePassword = source["sharePassword"];
	        this.combinedShare = source["combinedShare"];
	        this.importShare = source["importShare"];
	        this.manageCreatedShares = source["manageCreatedShares"];
	        this.editCreatedShares = source["editCreatedShares"];
	        this.cancelCreatedShares = source["cancelCreatedShares"];
	        this.manageImportedShares = source["manageImportedShares"];
	        this.shareHistory = source["shareHistory"];
	        this.quickTransfer = source["quickTransfer"];
	        this.favorite = source["favorite"];
	        this.encryption = source["encryption"];
	        this.playbackHistory = source["playbackHistory"];
	        this.copyTree = source["copyTree"];
	        this.photoAlbum = source["photoAlbum"];
	        this.provideHashes = source["provideHashes"];
	        this.rapidUploadHashes = source["rapidUploadHashes"];
	        this.uploadConflictPolicies = source["uploadConflictPolicies"];
	    }
	}
	export class DirPage {
	    items: model.File[];
	    nextMarker?: string;
	    total?: number;
	
	    static createFrom(source: any = {}) {
	        return new DirPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], model.File);
	        this.nextMarker = source["nextMarker"];
	        this.total = source["total"];
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
	export class FileRef {
	    id: string;
	    isDir?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.isDir = source["isDir"];
	    }
	}
	export class LoginOption {
	    value: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new LoginOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.label = source["label"];
	    }
	}
	export class LoginField {
	    key: string;
	    label: string;
	    type: string;
	    required: boolean;
	    hint?: string;
	    placeholder?: string;
	    options?: LoginOption[];
	
	    static createFrom(source: any = {}) {
	        return new LoginField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.type = source["type"];
	        this.required = source["required"];
	        this.hint = source["hint"];
	        this.placeholder = source["placeholder"];
	        this.options = this.convertValues(source["options"], LoginOption);
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
	export class LoginConfig {
	    fields: LoginField[];
	
	    static createFrom(source: any = {}) {
	        return new LoginConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fields = this.convertValues(source["fields"], LoginField);
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
	
	
	export class Meta {
	    key: string;
	    label: string;
	    icon: string;
	    rootKey?: string;
	    rootTitle?: string;
	
	    static createFrom(source: any = {}) {
	        return new Meta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.icon = source["icon"];
	        this.rootKey = source["rootKey"];
	        this.rootTitle = source["rootTitle"];
	    }
	}
	export class MkdirResult {
	    file_id: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new MkdirResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file_id = source["file_id"];
	        this.error = source["error"];
	    }
	}
	export class RenameResult {
	    file_id: string;
	    parent_file_id: string;
	    name: string;
	    isDir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RenameResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file_id = source["file_id"];
	        this.parent_file_id = source["parent_file_id"];
	        this.name = source["name"];
	        this.isDir = source["isDir"];
	    }
	}
	export class ShareImportFile {
	    fileId: string;
	    name: string;
	    size: number;
	    isDir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ShareImportFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileId = source["fileId"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.isDir = source["isDir"];
	    }
	}
	export class ShareImportSession {
	    provider: string;
	    shareUrl: string;
	    shareId: string;
	    password?: string;
	    passCodeToken?: string;
	    shareToken?: string;
	    shareKey?: string;
	    rootFileId?: string;
	    files: ShareImportFile[];
	    extra?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new ShareImportSession(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.shareUrl = source["shareUrl"];
	        this.shareId = source["shareId"];
	        this.password = source["password"];
	        this.passCodeToken = source["passCodeToken"];
	        this.shareToken = source["shareToken"];
	        this.shareKey = source["shareKey"];
	        this.rootFileId = source["rootFileId"];
	        this.files = this.convertValues(source["files"], ShareImportFile);
	        this.extra = source["extra"];
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
	export class ShareParams {
	    fileIds: string[];
	    fileRefs?: FileRef[];
	    shareName: string;
	    expiration?: string;
	    password?: string;
	
	    static createFrom(source: any = {}) {
	        return new ShareParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileIds = source["fileIds"];
	        this.fileRefs = this.convertValues(source["fileRefs"], FileRef);
	        this.shareName = source["shareName"];
	        this.expiration = source["expiration"];
	        this.password = source["password"];
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

export namespace model {
	
	export class Quota {
	    type: string;
	    size: number;
	    sizeStr: string;
	    used: number;
	    usedStr: string;
	    status?: string;
	    updated_at?: number;
	    expired?: string;
	    description?: string;
	
	    static createFrom(source: any = {}) {
	        return new Quota(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.size = source["size"];
	        this.sizeStr = source["sizeStr"];
	        this.used = source["used"];
	        this.usedStr = source["usedStr"];
	        this.status = source["status"];
	        this.updated_at = source["updated_at"];
	        this.expired = source["expired"];
	        this.description = source["description"];
	    }
	}
	export class ConnConfig {
	    name?: string;
	    endpoint?: string;
	    username?: string;
	    password?: string;
	    authType?: string;
	    rootPath?: string;
	    region?: string;
	    bucket?: string;
	    basePath?: string;
	    forcePathStyle?: boolean;
	    sessionToken?: string;
	    allowPrivateNetwork?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConnConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.endpoint = source["endpoint"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.authType = source["authType"];
	        this.rootPath = source["rootPath"];
	        this.region = source["region"];
	        this.bucket = source["bucket"];
	        this.basePath = source["basePath"];
	        this.forcePathStyle = source["forcePathStyle"];
	        this.sessionToken = source["sessionToken"];
	        this.allowPrivateNetwork = source["allowPrivateNetwork"];
	    }
	}
	export class TokenInfo {
	    tokenfrom: string;
	    access_token: string;
	    refresh_token: string;
	    token_type: string;
	    expires_in: number;
	    open_api_token_type: string;
	    open_api_access_token: string;
	    open_api_refresh_token: string;
	    open_api_expires_in: number;
	    signature?: string;
	    device_id?: string;
	    user_id?: string;
	    user_name?: string;
	    avatar?: string;
	    nick_name?: string;
	    name?: string;
	    role?: string;
	    status?: string;
	    state?: string;
	    expire_time?: string;
	    spu_id?: string;
	    default_drive_id?: string;
	    resource_drive_id?: string;
	    backup_drive_id?: string;
	    sbox_drive_id?: string;
	    pic_drive_id?: string;
	    default_sbox_drive_id?: string;
	    provider_account_id?: string;
	    provider_root_id?: string;
	    provider_drive_type?: string;
	    used_size?: number;
	    total_size?: number;
	    free_size?: number;
	    vipname?: string;
	    vipIcon?: string;
	    vipexpire?: string;
	    conn?: ConnConfig;
	
	    static createFrom(source: any = {}) {
	        return new TokenInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tokenfrom = source["tokenfrom"];
	        this.access_token = source["access_token"];
	        this.refresh_token = source["refresh_token"];
	        this.token_type = source["token_type"];
	        this.expires_in = source["expires_in"];
	        this.open_api_token_type = source["open_api_token_type"];
	        this.open_api_access_token = source["open_api_access_token"];
	        this.open_api_refresh_token = source["open_api_refresh_token"];
	        this.open_api_expires_in = source["open_api_expires_in"];
	        this.signature = source["signature"];
	        this.device_id = source["device_id"];
	        this.user_id = source["user_id"];
	        this.user_name = source["user_name"];
	        this.avatar = source["avatar"];
	        this.nick_name = source["nick_name"];
	        this.name = source["name"];
	        this.role = source["role"];
	        this.status = source["status"];
	        this.state = source["state"];
	        this.expire_time = source["expire_time"];
	        this.spu_id = source["spu_id"];
	        this.default_drive_id = source["default_drive_id"];
	        this.resource_drive_id = source["resource_drive_id"];
	        this.backup_drive_id = source["backup_drive_id"];
	        this.sbox_drive_id = source["sbox_drive_id"];
	        this.pic_drive_id = source["pic_drive_id"];
	        this.default_sbox_drive_id = source["default_sbox_drive_id"];
	        this.provider_account_id = source["provider_account_id"];
	        this.provider_root_id = source["provider_root_id"];
	        this.provider_drive_type = source["provider_drive_type"];
	        this.used_size = source["used_size"];
	        this.total_size = source["total_size"];
	        this.free_size = source["free_size"];
	        this.vipname = source["vipname"];
	        this.vipIcon = source["vipIcon"];
	        this.vipexpire = source["vipexpire"];
	        this.conn = this.convertValues(source["conn"], ConnConfig);
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
	export class Account {
	    user_id: string;
	    drive_id: string;
	    token?: TokenInfo;
	    order?: number;
	    disabled?: boolean;
	    custom_name?: string;
	    custom_icon?: string;
	    usage?: Quota;
	
	    static createFrom(source: any = {}) {
	        return new Account(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.token = this.convertValues(source["token"], TokenInfo);
	        this.order = source["order"];
	        this.disabled = source["disabled"];
	        this.custom_name = source["custom_name"];
	        this.custom_icon = source["custom_icon"];
	        this.usage = this.convertValues(source["usage"], Quota);
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
	
	export class DownloadTask {
	    id: string;
	    user_id: string;
	    drive_id: string;
	    provider: string;
	    file_id: string;
	    name: string;
	    size: number;
	    downloaded: number;
	    speed: number;
	    progress: number;
	    status: string;
	    localPath: string;
	    url?: string;
	    headers?: Record<string, string>;
	    error?: string;
	    created: number;
	    updated: number;
	    concurrency?: number;
	
	    static createFrom(source: any = {}) {
	        return new DownloadTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.provider = source["provider"];
	        this.file_id = source["file_id"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.downloaded = source["downloaded"];
	        this.speed = source["speed"];
	        this.progress = source["progress"];
	        this.status = source["status"];
	        this.localPath = source["localPath"];
	        this.url = source["url"];
	        this.headers = source["headers"];
	        this.error = source["error"];
	        this.created = source["created"];
	        this.updated = source["updated"];
	        this.concurrency = source["concurrency"];
	    }
	}
	export class DownloadURL {
	    drive_id: string;
	    file_id: string;
	    expire_time: number;
	    url: string;
	    size: number;
	    headers?: Record<string, string>;
	    downloadMode?: string;
	    forceLocalProxy?: boolean;
	    concurrency?: number;
	    chunkSize?: number;
	    allow_private_network?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DownloadURL(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.expire_time = source["expire_time"];
	        this.url = source["url"];
	        this.size = source["size"];
	        this.headers = source["headers"];
	        this.downloadMode = source["downloadMode"];
	        this.forceLocalProxy = source["forceLocalProxy"];
	        this.concurrency = source["concurrency"];
	        this.chunkSize = source["chunkSize"];
	        this.allow_private_network = source["allow_private_network"];
	    }
	}
	export class File {
	    drive_id: string;
	    file_id: string;
	    parent_file_id: string;
	    name: string;
	    namesearch: string;
	    path?: string;
	    ext: string;
	    mime_type: string;
	    mime_extension: string;
	    category: string;
	    icon: string;
	    file_count?: number;
	    size: number;
	    sizeStr: string;
	    time: number;
	    timeStr: string;
	    starred: boolean;
	    isDir: boolean;
	    thumbnail: string;
	    punish_flag?: number;
	    from_share_id?: string;
	    description: string;
	    content_hash?: string;
	    content_hash_name?: string;
	    crc64_hash?: string;
	    album_id?: string;
	    compilation_id?: string;
	    download_url?: string;
	    media_width?: number;
	    media_height?: number;
	    media_duration?: string;
	    media_play_cursor?: string;
	    media_time?: string;
	    user_meta?: string;
	
	    static createFrom(source: any = {}) {
	        return new File(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.parent_file_id = source["parent_file_id"];
	        this.name = source["name"];
	        this.namesearch = source["namesearch"];
	        this.path = source["path"];
	        this.ext = source["ext"];
	        this.mime_type = source["mime_type"];
	        this.mime_extension = source["mime_extension"];
	        this.category = source["category"];
	        this.icon = source["icon"];
	        this.file_count = source["file_count"];
	        this.size = source["size"];
	        this.sizeStr = source["sizeStr"];
	        this.time = source["time"];
	        this.timeStr = source["timeStr"];
	        this.starred = source["starred"];
	        this.isDir = source["isDir"];
	        this.thumbnail = source["thumbnail"];
	        this.punish_flag = source["punish_flag"];
	        this.from_share_id = source["from_share_id"];
	        this.description = source["description"];
	        this.content_hash = source["content_hash"];
	        this.content_hash_name = source["content_hash_name"];
	        this.crc64_hash = source["crc64_hash"];
	        this.album_id = source["album_id"];
	        this.compilation_id = source["compilation_id"];
	        this.download_url = source["download_url"];
	        this.media_width = source["media_width"];
	        this.media_height = source["media_height"];
	        this.media_duration = source["media_duration"];
	        this.media_play_cursor = source["media_play_cursor"];
	        this.media_time = source["media_time"];
	        this.user_meta = source["user_meta"];
	    }
	}
	export class MigrateItem {
	    sourceRecorded?: boolean;
	    sourceTime?: number;
	    sourceHash?: string;
	    targetId?: string;
	    isDir: boolean;
	    id: string;
	    name: string;
	    parentId: string;
	    status: string;
	    error?: string;
	    verification: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new MigrateItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceRecorded = source["sourceRecorded"];
	        this.sourceTime = source["sourceTime"];
	        this.sourceHash = source["sourceHash"];
	        this.targetId = source["targetId"];
	        this.isDir = source["isDir"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.parentId = source["parentId"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.verification = source["verification"];
	        this.size = source["size"];
	    }
	}
	export class MigrateJob {
	    items?: Record<string, MigrateItem>;
	    id: string;
	    srcUser: string;
	    srcDrive: string;
	    fileIDs: string[];
	    dstUser: string;
	    dstDrive: string;
	    dstParent: string;
	    move: boolean;
	    total: number;
	    processed: number;
	    failed: number;
	    totalBytes: number;
	    processedBytes: number;
	    status: string;
	    message?: string;
	    completedFileIDs?: string[];
	    copiedFileIDs?: string[];
	    targetDirectoryIDs?: Record<string, string>;
	    createdAt?: number;
	    updatedAt?: number;
	
	    static createFrom(source: any = {}) {
	        return new MigrateJob(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], MigrateItem, true);
	        this.id = source["id"];
	        this.srcUser = source["srcUser"];
	        this.srcDrive = source["srcDrive"];
	        this.fileIDs = source["fileIDs"];
	        this.dstUser = source["dstUser"];
	        this.dstDrive = source["dstDrive"];
	        this.dstParent = source["dstParent"];
	        this.move = source["move"];
	        this.total = source["total"];
	        this.processed = source["processed"];
	        this.failed = source["failed"];
	        this.totalBytes = source["totalBytes"];
	        this.processedBytes = source["processedBytes"];
	        this.status = source["status"];
	        this.message = source["message"];
	        this.completedFileIDs = source["completedFileIDs"];
	        this.copiedFileIDs = source["copiedFileIDs"];
	        this.targetDirectoryIDs = source["targetDirectoryIDs"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
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
	export class OfflineTask {
	    id: string;
	    user_id: string;
	    drive_id: string;
	    task_id?: string;
	    file_id?: string;
	    url?: string;
	    file_name?: string;
	    status: string;
	    progress: number;
	    message?: string;
	    file_size?: number;
	    created_time?: string;
	    updated_time?: string;
	    created: number;
	
	    static createFrom(source: any = {}) {
	        return new OfflineTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.task_id = source["task_id"];
	        this.file_id = source["file_id"];
	        this.url = source["url"];
	        this.file_name = source["file_name"];
	        this.status = source["status"];
	        this.progress = source["progress"];
	        this.message = source["message"];
	        this.file_size = source["file_size"];
	        this.created_time = source["created_time"];
	        this.updated_time = source["updated_time"];
	        this.created = source["created"];
	    }
	}
	
	export class ShareHistoryEntry {
	    share_id: string;
	    account_id: string;
	    drive_id: string;
	    file_id: string;
	    share_url: string;
	    share_pwd?: string;
	    share_name: string;
	    created_at: number;
	    provider: string;
	
	    static createFrom(source: any = {}) {
	        return new ShareHistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.share_id = source["share_id"];
	        this.account_id = source["account_id"];
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.share_url = source["share_url"];
	        this.share_pwd = source["share_pwd"];
	        this.share_name = source["share_name"];
	        this.created_at = source["created_at"];
	        this.provider = source["provider"];
	    }
	}
	export class ShareItem {
	    account_id?: string;
	    account_name?: string;
	    account_provider?: string;
	    share_key?: string;
	    created_at: string;
	    creator?: string;
	    description?: string;
	    display_name?: string;
	    display_label?: string;
	    download_count?: number;
	    drive_id?: string;
	    expiration?: string;
	    expired?: boolean;
	    file_id?: string;
	    file_id_list?: string[];
	    icon?: string;
	    preview_count?: number;
	    save_count?: number;
	    share_id: string;
	    share_msg?: string;
	    full_share_msg?: string;
	    share_name?: string;
	    share_policy?: string;
	    share_pwd?: string;
	    share_url?: string;
	    status?: string;
	    updated_at?: string;
	    is_share_saved?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ShareItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account_id = source["account_id"];
	        this.account_name = source["account_name"];
	        this.account_provider = source["account_provider"];
	        this.share_key = source["share_key"];
	        this.created_at = source["created_at"];
	        this.creator = source["creator"];
	        this.description = source["description"];
	        this.display_name = source["display_name"];
	        this.display_label = source["display_label"];
	        this.download_count = source["download_count"];
	        this.drive_id = source["drive_id"];
	        this.expiration = source["expiration"];
	        this.expired = source["expired"];
	        this.file_id = source["file_id"];
	        this.file_id_list = source["file_id_list"];
	        this.icon = source["icon"];
	        this.preview_count = source["preview_count"];
	        this.save_count = source["save_count"];
	        this.share_id = source["share_id"];
	        this.share_msg = source["share_msg"];
	        this.full_share_msg = source["full_share_msg"];
	        this.share_name = source["share_name"];
	        this.share_policy = source["share_policy"];
	        this.share_pwd = source["share_pwd"];
	        this.share_url = source["share_url"];
	        this.status = source["status"];
	        this.updated_at = source["updated_at"];
	        this.is_share_saved = source["is_share_saved"];
	    }
	}
	export class Subtitle {
	    language: string;
	    url: string;
	    headers?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new Subtitle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.language = source["language"];
	        this.url = source["url"];
	        this.headers = source["headers"];
	    }
	}
	
	export class UploadInfo {
	    localFilePath: string;
	    parent_file_id: string;
	    drive_id: string;
	    path: string;
	    name: string;
	    size: number;
	    sizeStr: string;
	    icon: string;
	    isDir: boolean;
	    isMiaoChuan: boolean;
	    sha1: string;
	    crc64: string;
	    conflictPolicy?: string;
	    cleanupLocalFile?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UploadInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.localFilePath = source["localFilePath"];
	        this.parent_file_id = source["parent_file_id"];
	        this.drive_id = source["drive_id"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.sizeStr = source["sizeStr"];
	        this.icon = source["icon"];
	        this.isDir = source["isDir"];
	        this.isMiaoChuan = source["isMiaoChuan"];
	        this.sha1 = source["sha1"];
	        this.crc64 = source["crc64"];
	        this.conflictPolicy = source["conflictPolicy"];
	        this.cleanupLocalFile = source["cleanupLocalFile"];
	    }
	}
	export class UploadState {
	    DownState: string;
	    DownTime: number;
	    DownSize: number;
	    DownSpeed: number;
	    DownSpeedStr: string;
	    DownProcess: number;
	    IsStop: boolean;
	    IsDowning: boolean;
	    IsCompleted: boolean;
	    IsFailed: boolean;
	    failedCode: number;
	    failedMessage: string;
	    AutoTry: number;
	    upload_id: string;
	    file_id: string;
	    IsBreakExist: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UploadState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.DownState = source["DownState"];
	        this.DownTime = source["DownTime"];
	        this.DownSize = source["DownSize"];
	        this.DownSpeed = source["DownSpeed"];
	        this.DownSpeedStr = source["DownSpeedStr"];
	        this.DownProcess = source["DownProcess"];
	        this.IsStop = source["IsStop"];
	        this.IsDowning = source["IsDowning"];
	        this.IsCompleted = source["IsCompleted"];
	        this.IsFailed = source["IsFailed"];
	        this.failedCode = source["failedCode"];
	        this.failedMessage = source["failedMessage"];
	        this.AutoTry = source["AutoTry"];
	        this.upload_id = source["upload_id"];
	        this.file_id = source["file_id"];
	        this.IsBreakExist = source["IsBreakExist"];
	    }
	}
	export class UploadingUI {
	    UploadID: string;
	    user_id: string;
	    Info: UploadInfo;
	    Upload: UploadState;
	
	    static createFrom(source: any = {}) {
	        return new UploadingUI(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.UploadID = source["UploadID"];
	        this.user_id = source["user_id"];
	        this.Info = this.convertValues(source["Info"], UploadInfo);
	        this.Upload = this.convertValues(source["Upload"], UploadState);
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
	export class VideoQuality {
	    html: string;
	    quality: string;
	    height: number;
	    width: number;
	    label: string;
	    value: string;
	    url: string;
	    type?: string;
	    expire_time?: number;
	    headers?: Record<string, string>;
	    forceProxy?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VideoQuality(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.html = source["html"];
	        this.quality = source["quality"];
	        this.height = source["height"];
	        this.width = source["width"];
	        this.label = source["label"];
	        this.value = source["value"];
	        this.url = source["url"];
	        this.type = source["type"];
	        this.expire_time = source["expire_time"];
	        this.headers = source["headers"];
	        this.forceProxy = source["forceProxy"];
	    }
	}
	export class VideoPreview {
	    drive_id: string;
	    file_id: string;
	    size: number;
	    duration: number;
	    expire_time: number;
	    width: number;
	    height: number;
	    headers?: Record<string, string>;
	    no_origin?: boolean;
	    qualities?: VideoQuality[];
	    subtitles?: Subtitle[];
	    url?: string;
	    current_quality?: string;
	    stream_type?: string;
	    allow_private_network?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VideoPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.size = source["size"];
	        this.duration = source["duration"];
	        this.expire_time = source["expire_time"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.headers = source["headers"];
	        this.no_origin = source["no_origin"];
	        this.qualities = this.convertValues(source["qualities"], VideoQuality);
	        this.subtitles = this.convertValues(source["subtitles"], Subtitle);
	        this.url = source["url"];
	        this.current_quality = source["current_quality"];
	        this.stream_type = source["stream_type"];
	        this.allow_private_network = source["allow_private_network"];
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

export namespace store {
	
	export class CachedSearchResult {
	    userId: string;
	    driveId: string;
	    parentId: string;
	    updatedAt: number;
	    file: model.File;
	
	    static createFrom(source: any = {}) {
	        return new CachedSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.userId = source["userId"];
	        this.driveId = source["driveId"];
	        this.parentId = source["parentId"];
	        this.updatedAt = source["updatedAt"];
	        this.file = this.convertValues(source["file"], model.File);
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
	export class Favorite {
	    user_id: string;
	    drive_id: string;
	    file_id: string;
	    name: string;
	    isDir: boolean;
	    added: number;
	    source?: string;
	    file?: model.File;
	
	    static createFrom(source: any = {}) {
	        return new Favorite(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.name = source["name"];
	        this.isDir = source["isDir"];
	        this.added = source["added"];
	        this.source = source["source"];
	        this.file = this.convertValues(source["file"], model.File);
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
	export class LocalTag {
	    user_id: string;
	    drive_id: string;
	    file_id: string;
	    name: string;
	    color: string;
	    tag_id: string;
	    created: number;
	
	    static createFrom(source: any = {}) {
	        return new LocalTag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.file_id = source["file_id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.tag_id = source["tag_id"];
	        this.created = source["created"];
	    }
	}
	export class Settings {
	    theme: string;
	    defaultTab: string;
	    proxy?: string;
	    downloadDir?: string;
	    maxConcurrentDownloads: number;
	    maxDownloadSpeed: number;
	    maxUploadSpeed: number;
	    autoUpdate: boolean;
	    confirmUpdate: boolean;
	    playbackResume: boolean;
	    keepTasks: boolean;
	    logLevel?: string;
	    closeToTray?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.defaultTab = source["defaultTab"];
	        this.proxy = source["proxy"];
	        this.downloadDir = source["downloadDir"];
	        this.maxConcurrentDownloads = source["maxConcurrentDownloads"];
	        this.maxDownloadSpeed = source["maxDownloadSpeed"];
	        this.maxUploadSpeed = source["maxUploadSpeed"];
	        this.autoUpdate = source["autoUpdate"];
	        this.confirmUpdate = source["confirmUpdate"];
	        this.playbackResume = source["playbackResume"];
	        this.keepTasks = source["keepTasks"];
	        this.logLevel = source["logLevel"];
	        this.closeToTray = source["closeToTray"];
	    }
	}

}

export namespace sync {
	
	export class Entry {
	    scope?: string;
	    modTimeNano?: number;
	    localTimeNano?: number;
	    paired?: boolean;
	    localSize?: number;
	    localTime?: number;
	    remoteSize?: number;
	    remoteTime?: number;
	    localPath: string;
	    remoteId: string;
	    remoteName: string;
	    size: number;
	    modTime: number;
	    hash?: string;
	    isDir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scope = source["scope"];
	        this.modTimeNano = source["modTimeNano"];
	        this.localTimeNano = source["localTimeNano"];
	        this.paired = source["paired"];
	        this.localSize = source["localSize"];
	        this.localTime = source["localTime"];
	        this.remoteSize = source["remoteSize"];
	        this.remoteTime = source["remoteTime"];
	        this.localPath = source["localPath"];
	        this.remoteId = source["remoteId"];
	        this.remoteName = source["remoteName"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	        this.hash = source["hash"];
	        this.isDir = source["isDir"];
	    }
	}
	export class Change {
	    path: string;
	    action: string;
	    local?: Entry;
	    remote?: Entry;
	
	    static createFrom(source: any = {}) {
	        return new Change(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.action = source["action"];
	        this.local = this.convertValues(source["local"], Entry);
	        this.remote = this.convertValues(source["remote"], Entry);
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
	export class Config {
	    conflictPolicy?: string;
	    id: string;
	    name: string;
	    user_id: string;
	    drive_id: string;
	    local_dir: string;
	    remote_dir: string;
	    remote_name: string;
	    direction: string;
	    enabled: boolean;
	    intervalMin: number;
	    deletePropagation: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conflictPolicy = source["conflictPolicy"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.user_id = source["user_id"];
	        this.drive_id = source["drive_id"];
	        this.local_dir = source["local_dir"];
	        this.remote_dir = source["remote_dir"];
	        this.remote_name = source["remote_name"];
	        this.direction = source["direction"];
	        this.enabled = source["enabled"];
	        this.intervalMin = source["intervalMin"];
	        this.deletePropagation = source["deletePropagation"];
	    }
	}
	
	export class Plan {
	    snapshotTotal: number;
	    token: string;
	    changes: Change[];
	    blocked?: string;
	
	    static createFrom(source: any = {}) {
	        return new Plan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.snapshotTotal = source["snapshotTotal"];
	        this.token = source["token"];
	        this.changes = this.convertValues(source["changes"], Change);
	        this.blocked = source["blocked"];
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

