; Inno Setup Script for Mnemo
; Build: wails build -platform windows/amd64, then ISCC this script.
; Pass /DMyArchSuffix=Mnemo-windows-x64 to set the output filename.

#ifndef MyArchSuffix
  #define MyArchSuffix "MnemoSetup"
#endif

#define MyAppName "Mnemo"
#ifndef MyAppVersion
  #define MyAppVersion "0.3.0"
#endif
#define MyAppExeName "Mnemo.exe"

[Setup]
AppId={{8F4E3D2A-1B5C-4E7F-9A2D-6C8B1F3E5D7A}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppName}
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=..\installer
OutputBaseFilename={#MyArchSuffix}-Setup
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
#if Pos("arm64", MyArchSuffix) > 0
ArchitecturesInstallIn64BitMode=arm64
ArchitecturesAllowed=arm64
#else
ArchitecturesInstallIn64BitMode=x64compatible
ArchitecturesAllowed=x64compatible
#endif
UninstallDisplayIcon={app}\{#MyAppExeName}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\bin\Mnemo.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\bin\data\secrets.json"; DestDir: "{app}\data"; Flags: ignoreversion skipifsourcedoesntexist

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\{cm:UninstallProgram,{#MyAppName}}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#MyAppName}}"; Flags: nowait postinstall skipifsilent
Filename: "{app}\{#MyAppExeName}"; Flags: nowait runasoriginaluser; Check: ShouldRestartAfterUpdate

[Code]
function ShouldRestartAfterUpdate: Boolean;
begin
  Result := WizardSilent and (ExpandConstant('{param:MNEMORESTART|0}') = '1');
end;
