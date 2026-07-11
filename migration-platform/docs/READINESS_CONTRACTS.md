# Readiness e contratti read-only

## Scopo

Il readiness report descrive se esistono evidenze sufficienti per progettare un
writer reale. Non abilita writer, non accoda actor e non autorizza scritture.
Ogni report è legato a `migration_id`, `plan_id`, `comparison_report_id`,
`source_snapshot_id` e `destination_snapshot_id` esatti.

## Stati

- `not_ready`: manca un requisito di sicurezza o un contratto fondamentale.
- `needs_inventory`: almeno un dato necessario non è leggibile/verificabile.
- `needs_contract_test`: inventario completo, contratto writer ancora da provare.
- `needs_operator_input`: serviranno password, approval o decisioni umane.
- `eligible_for_real_design`: evidenze read-only sufficienti per progettare il
  writer; non significa writer disponibile, abilitato o verificato.

La priorità conservativa impedisce a un requisito meno grave di nascondere un
blocker più forte. La generazione rifiuta piani, comparazioni o snapshot obsoleti.

## Pipeline delle evidenze

```text
preflight read-only
  -> snapshot sorgente/destinazione immutabili
  -> comparazione legata agli snapshot
  -> piano (evidenze tecniche sempre excluded)
  -> readiness report persistente
```

Una conferma operatore non crea evidenza e non produce mai `verified`.

## Contratti implementati

### Database MySQL

`database_contract` combina:

- `Mysql::get_restrictions`;
- `maximum_databases` da `Variables::get_user_information`;
- conteggio corrente da `Mysql::list_databases`.

La coverage è `succeeded` solo con restrizioni non vuote e quota nota. Il
readiness richiede successo sia su sorgente sia su destinazione.

### Utenti e grant MySQL

`mysql_grants` interroga `Mysql::get_privileges_on_database` per ogni prodotto
utente×database. Conserva soltanto database, utente e privilegi normalizzati.
Fallimenti parziali non diventano una matrice vuota.

`mysql_grant_contract` richiede:

- `pairs_checked == pairs_total`;
- nessun fallimento di lettura;
- privilegi appartenenti all'insieme account-level supportato.

`mysql_grants` e `mysql_grant_contract` sono evidenze, quindi il planner li
marca `excluded` e non crea operazioni duplicate.

### FTP

Gli account migrabili devono avere quota e home directory esplicite da
`Ftp::list_ftp_with_disk`. Il completamento rimuove il gap di inventario, ma il
writer resta `needs_contract_test`.

### Mailing list

`private` è accettato solo se esplicito. La normalizzazione usa, in ordine:

1. `private` o `listtype` dalla risposta UAPI;
2. campi `archive_private`, `advertised`, `subscribe_policy`;
3. fallback read-only API 2 `Email::listlists` con gli stessi campi.

La provenienza è registrata in `_privacy_source`. Nessun valore implicito viene
inventato; in assenza di evidenza la coverage resta `partial`.

### DNS

`DNS::parse_zone` interroga soltanto dominio principale, addon e alias. I
sottodomini sono record della zona genitore e non zone autonome. Restano da
implementare collision detection e fresh zone verification pre-write.

## Dati esclusi

Readiness e contract evidence non devono contenere token, password, ciphertext,
chiavi Fernet, `auth_secret`, chiavi SSL private o body/subject/from degli
autoresponder. Password e contenuti sensibili non devono apparire neppure nei
messaggi di errore o negli eventi aggregati.

## Stato pilota verificato

- job preflight `17`;
- snapshot `33/34`;
- comparazione `18`;
- piano `12`;
- readiness `9`;
- `needs_inventory=0`;
- database e utenti MySQL `eligible_for_real_design`;
- FTP e mailing list `needs_contract_test`;
- DNS `not_ready` per collisioni/fresh verification;
- tutti i writer e `MOCK_ORCHESTRATOR_MODE` disabilitati.

Gli ID sono storici: rileggerli sempre dalle API prima di usarli.

## Prossimi contratti

1. FTP: validazione completa degli argomenti quota/home del futuro writer.
2. Mailing list: mapping `private` verso `Email::add_list`.
3. Forwarder e autoresponder: fresh read anti-upsert.
4. DNS: collisioni, record differenti e rilettura fresca della zona.

Ogni futuro percorso reale richiederà autorizzazione esplicita separata,
destinazione-only, conferma forte, nessun overwrite/delete implicito e nuovo
preflight/comparazione post-write.
