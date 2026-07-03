# Fix cpapi2 jailshell su .78 — prompt per sessione con root SSH

Copia-incolla da qui in giù in una sessione con accesso root SSH diretto
a 38.224.109.78.

---

## Problema

Il server .78 (dest sacrificale, cPanel v11.110.0.133) ha il binario
`/usr/local/cpanel/cpanel` ripristinato dall'auto-update (2026-07-03
04:22), ma l'utente `giorginisposi` usa **jailshell** e il filesystem
virtualizzato della jailshell NON include `/usr/local/cpanel/cpanel`.
Di conseguenza `cpapi2` (symlink → `apitool`) fallisce con:

```
Failed to execute /usr/local/cpanel/cpanel: No such file or directory
at bin/apitool.pl line 278.
```

Questo blocca `cpapi2 Email setmxcheck` (l'unica primitiva per il
routing email, nessun equivalente UAPI). Il DNS writer (UAPI
`DNS::mass_edit_zone`) NON è impattato.

## Diagnosi (da fare come root)

```bash
# 1. Conferma che il binario esiste
ls -la /usr/local/cpanel/cpanel

# 2. Verifica la shell dell'utente
grep giorginisposi /etc/passwd
# Atteso: .../jailshell

# 3. Verifica cosa vede l'utente dentro la jailshell
su - giorginisposi -s /bin/bash -c 'ls -la /usr/local/cpanel/cpanel 2>&1'
# Atteso: No such file or directory

# 4. Verifica se CageFS è attivo
rpm -q cagefs 2>&1
cagefsctl --display-user-mode giorginisposi 2>&1
```

## Fix (incrementale, dal meno invasivo)

### Opzione A — aggiornare la jailshell skeleton (preferita)

```bash
# Rigenerare la jailshell skeleton per includere il nuovo binario
/usr/local/cpanel/bin/jailshell --installshell 2>&1

# Verificare
su - giorginisposi -s /bin/bash -c '/usr/local/cpanel/bin/cpapi2 --output=json Email listmxs 2>&1'
```

### Opzione B — se CageFS, aggiornare il template

```bash
cagefsctl --force-update 2>&1
cagefsctl --remount giorginisposi 2>&1

# Verificare
su - giorginisposi -s /bin/bash -c '/usr/local/cpanel/bin/cpapi2 --output=json Email listmxs 2>&1'
```

### Opzione C — cambiare la shell dell'utente (fallback)

```bash
# Solo se A e B non funzionano. Meno sicuro (niente jailshell).
/usr/local/cpanel/scripts/modifyacct giorginisposi shell=/bin/bash

# Verificare
su - giorginisposi -s /bin/bash -c '/usr/local/cpanel/bin/cpapi2 --output=json Email listmxs 2>&1'

# Per ripristinare dopo:
# /usr/local/cpanel/scripts/modifyacct giorginisposi shell=/usr/local/cpanel/bin/jailshell
```

## Verifica finale (OBBLIGATORIA)

Dopo il fix, verificare:

```bash
# 1. cpapi2 funziona dall'utente
su - giorginisposi -s /bin/bash -c '/usr/local/cpanel/bin/cpapi2 --output=json Email setmxcheck domain=giorginisposi.it mxcheck=local 2>&1'

# 2. uapi funziona ancora
su - giorginisposi -s /bin/bash -c 'uapi --output=json Email list_mxs 2>&1'

# 3. Peer NS standalone (CRITICO — regola cluster)
cat /var/cpanel/cluster/root/config/giorginisposi.it 2>/dev/null
# Oppure:
whmapi1 configclusterserver server=136.144.242.119 2>&1 | grep role
# Atteso: role standalone
```

## Poi (nella sessione di sviluppo cpanel-self-migration)

Con cpapi2 funzionante, il tool può fare:
1. Smoke live di `SetMXCheck` sul sacrificale (set routing → verify →
   rollback → verify = pre-smoke)
2. Addendum a `PR2B_3_SMOKE.md` che chiude il debito setmxcheck
3. Aggiornamento `CPAPI2_DIAGNOSIS_78.md` con causa radice e fix

## ⚠️ NON FARE

- Non toccare il cluster DNS (ruoli, peer, sync/standalone)
- Non toccare altri account oltre giorginisposi
- Non fare `removeacct` o `killdns`
- Non aggiornare cPanel (`upcp`) se non strettamente necessario
