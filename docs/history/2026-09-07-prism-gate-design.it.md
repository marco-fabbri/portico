# Prism Gate

Come rendere una Prism Central **multi-tenant** ed esporla su Internet,
tenendo gli account locali (a partire da `admin`) utilizzabili solo dalla
rete interna.

Stato del documento: **disegno**, scritto il 07/09/2026. La parte sui
meccanismi di Prism Central e' come il prodotto e' fatto in generale; i
nomi esatti di endpoint, ruoli e voci di menu cambiano con la versione e
vanno verificati sull'istanza vera (sezioni 4.3 e 9).

---

## 1. Cosa vuol dire "gated multi-tenant"

Una sola Prism Central, un solo indirizzo pubblico, e tre garanzie:

1. ogni tenant entra e vede **solo le proprie risorse**;
2. l'amministrazione della piattaforma non e' raggiungibile da fuori;
3. gli account locali di Prism Central non sono utilizzabili da fuori,
   nemmeno conoscendo la password.

La prima garanzia la da' Prism Central con progetti e ruoli. La seconda e
la terza le da' un reverse proxy davanti, il "cancello".

## 2. Il modello di multi-tenancy di Prism Central

Prism Central non ha tenant come oggetti separati: usa i **progetti**.
Un progetto e' un contenitore che tiene insieme utenti, quote e risorse.

| pezzo | a cosa serve nel nostro caso |
|---|---|
| progetto | il tenant: un progetto per cliente o per team |
| ruoli | cosa puo' fare un utente dentro il suo progetto |
| quote | quanta CPU, memoria e spazio puo' consumare il tenant |
| categorie | etichette sulle risorse, usate dalle politiche |
| reti | ogni tenant ha le proprie reti, assegnate al suo progetto |
| immagini | il catalogo che il tenant puo' usare per creare macchine |

Un utente vede solo cio' che appartiene ai progetti di cui fa parte.
Chi non e' in nessun progetto e non ha ruoli globali non vede nulla:
questo e' il comportamento su cui poggia tutta l'esposizione.

## 3. Le identita'

Regola: **i tenant non hanno account locali**. Si configura Prism Central
con un fornitore di identita' esterno (SAML, oppure LDAP o Active
Directory) e si mappano i **gruppi** dell'IdP sui progetti e sui ruoli.

Conseguenze pratiche:
- l'aggiunta e la rimozione di una persona si fanno nell'IdP, non in
  Prism Central;
- il filtro del cancello diventa semplice: da fuori si accetta solo
  l'autenticazione federata, e gli account locali restano per chi entra
  dalla rete interna;
- niente ruoli globali agli utenti dei tenant: solo ruoli dentro il
  proprio progetto. Un ruolo di amministratore della piattaforma dato a
  un'identita' federata vanifica tutto il resto.

## 4. Il cancello

### 4.1 Disegno

Due ascolti distinti sullo stesso proxy, verso la stessa Prism Central:

| ascolto | chi | cosa passa |
|---|---|---|
| interno (LAN o VPN) | amministrazione della piattaforma | tutto, compreso il login locale |
| pubblico | tenant | solo autenticazione federata |

Vincolo che regge tutto: **la 9440 deve essere raggiungibile solo dal
proxy**, a livello di firewall. Senza questo, ogni filtro si aggira
andando diretti.

### 4.2 Cosa si blocca sull'ascolto pubblico

**Autenticazione di base sulle API.** Ogni chiamata porta
`Authorization: Basic <base64 di utente:password>`. `admin:` e' lungo sei
byte, multiplo di tre, quindi in base64 diventa sempre `YWRtaW46`,
qualunque sia la password: il prefisso identifica l'utente in modo esatto.

**Login con il modulo.** Il nome utente sta nel corpo della richiesta, che
il proxy non legge comodamente: si blocca l'endpoint intero. I percorsi
dell'autenticazione federata sono altri e restano permessi.

```nginx
map $http_authorization $deny_basic {
    default 0;
    "~^Basic YWRtaW46"  1;    # solo l'utente admin
    # "~*^Basic "       1;    # variante robusta: qualunque account locale
}

server {                                   # ---- pubblico: i tenant
    listen 443 ssl;
    server_name prism.esempio.it;

    if ($deny_basic) { return 403; }
    location = /PrismGateway/j_spring_security_check { return 403; }

    location / {
        proxy_pass https://PRISM-CENTRAL:9440;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}

server {                                   # ---- interno: amministrazione
    listen 10.0.0.1:443 ssl;
    server_name prism-admin.interno;
    location / { proxy_pass https://PRISM-CENTRAL:9440; }
}
```

La variante che blocca qualunque `Basic` e' piu' solida, perche' non
dipende dal nome dell'utente; si puo' usare solo se i tenant non chiamano
le API dall'esterno. Se le chiamano: bloccare per prefisso i soli account
privilegiati, oppure decodificare l'intestazione con njs o Lua e
consentire una lista di nomi.

Rinforzo consigliato: davanti al tutto un proxy che autentica lui stesso
(oauth2-proxy, Authelia, un accesso applicativo di rete). Cosi' chi non ha
gia' un'identita' valida non raggiunge nemmeno la 9440.

### 4.3 Come ricavare i percorsi esatti, prima di esporre

1. strumenti di sviluppo del browser, scheda rete;
2. login **locale**: annota la richiesta che porta le credenziali;
3. logout, login **federato**: annota i reindirizzamenti e la richiesta
   di ritorno;
4. blocca la prima, permetti la seconda;
5. ripeti dopo ogni aggiornamento: nelle versioni recenti l'autenticazione
   passa da un sottosistema di identita' con endpoint propri.

## 5. Procedura, dall'inizio alla fine

1. **Identita'**: collega Prism Central all'IdP e verifica un login
   federato dalla rete interna.
2. **Progetti**: un progetto per tenant. Assegna al progetto le sue reti,
   le sue immagini e le sue quote.
3. **Ruoli**: mappa i gruppi dell'IdP sui ruoli dentro il progetto. Nessun
   ruolo globale ai tenant.
4. **Prova di visibilita'**: entra come utente di un tenant e verifica che
   non veda cluster, host, altri progetti, immagini altrui.
5. **Cancello**: metti il proxy, chiudi la 9440 al resto del mondo,
   applica i due divieti, ricava i percorsi con 4.3.
6. **Prove del cancello** (sezione 6).
7. **Operativita'**: definisci come si aggiunge un tenant (progetto,
   gruppo nell'IdP, rete, quota) e scrivilo, perche' diventera' un
   percorso automatizzabile.

## 6. Verifica

- da fuori, `curl -k -u admin:<password> https://prism.esempio.it/api/...`
  deve rispondere 403 dal proxy, non 401 da Prism Central;
- da fuori, il login federato di un tenant deve funzionare fino alla
  console, e mostrare solo le sue risorse;
- da fuori, il login locale con il modulo deve essere rifiutato;
- da dentro, l'amministrazione deve restare intatta;
- da fuori, la 9440 non deve rispondere affatto;
- un utente rimosso dal gruppo nell'IdP non deve piu' entrare.

## 7. Cosa resta all'amministratore, e da dove

Creazione e cancellazione dei progetti, quote, reti, catalogo delle
immagini, aggiornamenti, allarmi, licenze: tutto dall'ascolto interno.
Il tenant non ha modo di raggiungere quelle funzioni nemmeno indovinando
gli indirizzi, perche' il suo ruolo non glielo consente: il cancello e'
la seconda difesa, non la prima.

## 8. Limiti da dichiarare a chi userà il servizio

- **Il ruolo conta piu' della porta**: un'identita' federata con ruolo
  amministrativo e' amministratore anche da fuori.
- **Ripiego sull'autenticazione locale**: alcune installazioni tornano
  agli account locali quando l'IdP non risponde. E' il caso in cui il
  filtro sul proxy diventa l'unica difesa.
- **Isolamento logico, non fisico**: progetti e ruoli separano la
  visibilita' e il consumo, ma il piano dati e' condiviso. Per clienti che
  non si conoscono tra loro, valutare un cluster per cliente.
- **Superficie residua**: chiudere la 22 verso l'esterno, valutare un
  certificato client sull'ascolto interno, limitare la frequenza delle
  richieste sulla porta pubblica.

## 9. Da misurare in laboratorio

- l'elenco reale degli endpoint di login, locale e federato, per la
  versione in uso;
- se esista dentro Prism Central un modo di limitare per rete gli account
  locali, cosi' da non dipendere solo dal proxy;
- cosa mostra la console quando il login locale e' bloccato: errore
  chiaro o pagina rotta;
- il comportamento del portale self service dietro il proxy;
- quali chiamate fa davvero l'interfaccia di un tenant, per capire se una
  lista di percorsi permessi sia praticabile o solo fragile;
- quanto costa in risorse una Prism Central con piu' progetti attivi.
