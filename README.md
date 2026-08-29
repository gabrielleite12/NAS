# NAS Local - Gerenciador de Arquivos

Aplicativo de gerenciamento de arquivos NAS via interface web.

## Pré-requisitos

- **Go** (para compilar): `sudo apt install golang`
- **smbclient** (para verificar espaço em disco): `sudo apt install smbclient`

## Compilar

```bash
cd /media/leite/00C4C511C4C509BC/NAS
go build -o nas-manager .
```

Isso gera o executável `nas-manager` na mesma pasta.

## Executar

```bash
./nas-manager
```

O servidor inicia na porta configurada em `config.json` (padrão: **8765**) e abre o navegador automaticamente em `http://localhost:8765`.

---

## Iniciar Automaticamente no Linux Mint

### Opção 1: Criar um atalho `.desktop` (Recomendado)

Crie o arquivo `~/.local/share/applications/nas-manager.desktop`:

```bash
cat > ~/.local/share/applications/nas-manager.desktop << 'EOF'
[Desktop Entry]
Name=NAS Local
Comment=Gerenciador de Arquivos NAS
Exec=/media/leite/00C4C511C4C509BC/NAS/nas-manager
Path=/media/leite/00C4C511C4C509BC/NAS
Icon=folder
Terminal=false
Type=Application
Categories=Utility;FileManager;
StartupNotify=true
EOF
```

Depois, para aparecer no menu do Cinnamon, reinicie o painel ou faça logout/login.

Para iniciar com o sistema, copie para autostart:

```bash
cp ~/.local/share/applications/nas-manager.desktop ~/.config/autostart/
```

### Opção 2: Criar um script executável

```bash
# Criar script
cat > /media/leite/00C4C511C4C509BC/NAS/iniciar-nas.sh << 'EOF'
#!/bin/bash
cd /media/leite/00C4C511C4C509BC/NAS
./nas-manager
EOF

# Tornar executável
chmod +x /media/leite/00C4C511C4C509BC/NAS/iniciar-nas.sh
```

Agora basta dar duplo clique no `iniciar-nas.sh` para iniciar.

### Opção 3: Serviço systemd (roda em segundo plano)

```bash
sudo tee /etc/systemd/system/nas-manager.service << 'EOF'
[Unit]
Description=NAS Local Manager
After=network.target

[Service]
Type=simple
User=leite
WorkingDirectory=/media/leite/00C4C511C4C509BC/NAS
ExecStart=/media/leite/00C4C511C4C509BC/NAS/nas-manager
Restart=on-failure
Environment=DISPLAY=:0

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable nas-manager
sudo systemctl start nas-manager
```

## Funcionalidades

- 📁 **Criar Pastas** - Botão "+ Pasta" ou clique direito na área vazia
- 📄 **Criar Arquivos** - Clique direito na área vazia → "Novo Arquivo"
- 📥 **Drag & Drop** - Arraste arquivos do seu computador para a janela do navegador
- ⬆️ **Upload** - Botão "Upload" na barra de ações
- ✏️ **Renomear** - Clique direito no arquivo → "Renomear"
- 🗑️ **Excluir** - Clique direito no arquivo → "Excluir"
- ⬇️ **Download** - Clique direito no arquivo → "Baixar"
- 🔍 **Busca** - Campo de pesquisa na barra de ações
- 📋 **Logs** - Registro de todas as atividades
