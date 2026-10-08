package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	_ "time/tzdata" // zonas horarias embebidas (hora SLT sin depender del sistema)
)

const (
	urlXundra       = "https://reef.xs-pets.com"
	urlCatalogo     = "https://reef.xs-pets.com/fish"
	ttlDetalle      = 2 * time.Minute
	esperaCatalogo  = 15 * time.Second
	reintentoInicio = 30 * time.Second
	prefijoHuevo    = "(fish egg)"
	maxParalelo     = 4
	maxFilasPantalla = 60
	maxRare          = 5
)

var (
	// Catálogo: pez base en minúsculas -> URL de detalle ("" si el enlace no tiene href).
	mutex        sync.RWMutex
	enlacesPeces = make(map[string]string)

	catalogoListo = make(chan struct{})
	catalogoOnce  sync.Once

	// Detalle por pez base + descargas en curso (una sola por pez a la vez).
	detallesMu sync.Mutex
	detalles   = make(map[string]detalleCache)
	enCurso    = make(map[string]*llamada)

	clienteHTTP = &http.Client{Timeout: 10 * time.Second}

	errRed = errors.New("error de red con Xundra")
)

type stockVariante struct {
	Nombre  string
	EggsTxt string
	FishTxt string
	Eggs    int
	Fish    int
}

type detalleCache struct {
	variantes map[string]stockVariante // nombre completo en minúsculas
	orden     []string                 // claves en el orden de la tabla de la web
	momento   time.Time
}

type llamada struct {
	done chan struct{}
	d    detalleCache
	enc  bool
	err  error
}

type filaPantalla struct {
	Nombre   string
	LocEggs  int
	LocFish  int
	GlobEggs int
	GlobFish int
}

// Hora de Second Life (SLT = hora del Pacífico).
var zonaSLT = func() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.FixedZone("SLT", -8*3600)
}()

// ===================================================
//  FORMATO DE ANCHO FIJO (lo que antes hacía el script de la pantalla)
// ===================================================
func padDer(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func padIzq(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return strings.Repeat(" ", n-len(r)) + s
}

func centrar(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return padDer(strings.Repeat(" ", (n-len(r))/2)+s, n)
}

func recortar(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-2]) + ".."
}

// ===================================================
//  UTILIDADES
// ===================================================
func absoluto(href string) string {
	if strings.HasPrefix(href, "http") {
		return href
	}
	return urlXundra + "/" + strings.TrimPrefix(href, "/")
}

func descargar(url string) (*goquery.Document, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := clienteHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return goquery.NewDocumentFromReader(resp.Body)
}

func aEntero(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func esperarCatalogo() bool {
	select {
	case <-catalogoListo:
		return true
	case <-time.After(esperaCatalogo):
		return false
	}
}

func enCatalogo(pezBase string) bool {
	mutex.RLock()
	_, ok := enlacesPeces[strings.ToLower(strings.TrimSpace(pezBase))]
	mutex.RUnlock()
	return ok
}

// ===================================================
//  CATÁLOGO
// ===================================================
func actualizarCatalogo() bool {
	doc, err := descargar(urlCatalogo)
	if err != nil {
		log.Println("⚠️ ERROR crítico al conectar con Xundra:", err)
		return false
	}

	nuevos := make(map[string]string)
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		nombrePez := strings.TrimSpace(s.Text())
		if nombrePez == "" {
			return
		}
		clave := strings.ToLower(nombrePez)
		// Igual que el código original: si el nombre aparece varias veces, gana el último enlace.
		if href, ok := s.Attr("href"); ok {
			nuevos[clave] = absoluto(href)
		} else if _, existe := nuevos[clave]; !existe {
			nuevos[clave] = ""
		}
	})

	if len(nuevos) == 0 {
		log.Println("⚠️ Catálogo vacío, se mantiene el anterior.")
		return false
	}

	mutex.Lock()
	enlacesPeces = nuevos
	mutex.Unlock()

	catalogoOnce.Do(func() { close(catalogoListo) })
	log.Printf("✅ Catálogo base sincronizado con éxito: %d peces encontrados.", len(nuevos))
	return true
}

func iniciarActualizadorAutomatico() {
	for !actualizarCatalogo() {
		time.Sleep(reintentoInicio)
	}
	ticker := time.NewTicker(5 * time.Hour)
	go func() {
		for range ticker.C {
			actualizarCatalogo()
		}
	}()
}

// ===================================================
//  DETALLE DE CADA PEZ (caché + una sola descarga por pez + stale-while-revalidate)
// ===================================================

// obtenerDetalle devuelve el stock de todas las variantes de un pez base.
//   - Caché vigente: respuesta inmediata.
//   - Caché caducada: respuesta inmediata con la copia anterior y refresco en segundo plano.
//   - Sin caché: espera a la descarga (compartida con cualquier otra petición simultánea).
// encontrado=false: el pez no está en el catálogo. err=errRed: falló la descarga y no hay copia.
func obtenerDetalle(pezBase string) (detalleCache, bool, error) {
	clave := strings.ToLower(strings.TrimSpace(pezBase))

	detallesMu.Lock()
	previo, hayPrevio := detalles[clave]
	if hayPrevio && time.Since(previo.momento) < ttlDetalle {
		detallesMu.Unlock()
		return previo, true, nil
	}
	c, yaEnCurso := enCurso[clave]
	if !yaEnCurso {
		c = &llamada{done: make(chan struct{})}
		enCurso[clave] = c
		go descargarDetalle(clave, c)
	}
	detallesMu.Unlock()

	if hayPrevio {
		return previo, true, nil
	}
	<-c.done
	return c.d, c.enc, c.err
}

func descargarDetalle(clave string, c *llamada) {
	defer func() {
		detallesMu.Lock()
		delete(enCurso, clave)
		detallesMu.Unlock()
		close(c.done)
	}()

	mutex.RLock()
	enlace, existe := enlacesPeces[clave]
	mutex.RUnlock()

	if !existe || enlace == "" {
		detallesMu.Lock()
		delete(detalles, clave)
		detallesMu.Unlock()
		return
	}

	doc, err := descargar(enlace)
	if err != nil {
		log.Printf("⚠️ Error descargando detalle de %s: %v", clave, err)
		c.enc = true
		c.err = errRed
		return
	}

	nuevo := detalleCache{variantes: make(map[string]stockVariante), momento: time.Now()}
	doc.Find("tr").Each(func(_ int, fila *goquery.Selection) {
		var celdas []string
		fila.Find("td").Each(func(_ int, cel *goquery.Selection) {
			celdas = append(celdas, strings.TrimSpace(cel.Text()))
		})
		if len(celdas) >= 3 && celdas[0] != "" {
			k := strings.ToLower(celdas[0])
			if _, ya := nuevo.variantes[k]; !ya {
				nuevo.orden = append(nuevo.orden, k)
			}
			nuevo.variantes[k] = stockVariante{
				Nombre:  celdas[0],
				EggsTxt: celdas[1],
				FishTxt: celdas[2],
				Eggs:    aEntero(celdas[1]),
				Fish:    aEntero(celdas[2]),
			}
		}
	})

	detallesMu.Lock()
	detalles[clave] = nuevo
	detallesMu.Unlock()

	c.d = nuevo
	c.enc = true
}

// detallesEnParalelo obtiene el detalle de varios peces base a la vez (máx. maxParalelo).
func detallesEnParalelo(bases map[string]bool) map[string]detalleCache {
	resultados := make(map[string]detalleCache, len(bases))
	var resMu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParalelo)

	for base := range bases {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if d, encontrado, err := obtenerDetalle(b); encontrado && err == nil {
				resMu.Lock()
				resultados[b] = d
				resMu.Unlock()
			}
		}(base)
	}
	wg.Wait()
	return resultados
}

// ===================================================
//  /api/filtrar_menu  (HUD manual — misma entrada y salida)
// ===================================================
func filtrarMenuHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	nombresRecibidos := r.FormValue("objetos")

	if nombresRecibidos == "" || !esperarCatalogo() {
		w.Write([]byte("VACIO"))
		return
	}

	// 1ª pasada: limpiar, validar pez base y reunir los que necesitan detalle.
	type candidato struct {
		obj, base   string
		conVariante bool
	}
	var candidatos []candidato
	bases := make(map[string]bool)

	for _, obj := range strings.Split(nombresRecibidos, "|||") {
		obj = strings.TrimSpace(obj)
		if obj == "" {
			continue
		}
		partes := strings.SplitN(obj, ":", 2)
		pezBase := strings.TrimSpace(partes[0])

		if !enCatalogo(pezBase) {
			log.Printf("❌ Descartado (Pez base no existe): %s", pezBase)
			continue
		}
		base := strings.ToLower(pezBase)
		candidatos = append(candidatos, candidato{obj, base, len(partes) > 1})
		if len(partes) > 1 {
			bases[base] = true
		}
	}

	// Descarga en paralelo de los detalles necesarios.
	detallesBase := detallesEnParalelo(bases)

	// 2ª pasada: validar variantes y quitar duplicados, conservando el orden de entrada.
	var validos []string
	vistos := make(map[string]bool)

	for _, c := range candidatos {
		k := strings.ToLower(c.obj)
		if c.conVariante {
			d, ok := detallesBase[c.base]
			if !ok {
				log.Printf("❌ Descartado (Variante no oficial): [%s]", c.obj)
				continue
			}
			if _, oficial := d.variantes[k]; !oficial {
				log.Printf("❌ Descartado (Variante no oficial): [%s]", c.obj)
				continue
			}
		}
		if vistos[k] {
			continue
		}
		vistos[k] = true
		validos = append(validos, c.obj)
		log.Printf("✅ Aceptado como válido: %s", c.obj)
	}

	if len(validos) == 0 {
		w.Write([]byte("VACIO"))
		return
	}
	w.Write([]byte(strings.Join(validos, "\n")))
}

// ===================================================
//  /api/pez  (HUD manual — misma entrada y salida)
// ===================================================
func consultarExistenciasHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	entradaUsuario := r.FormValue("pez")
	if entradaUsuario == "" {
		http.Error(w, "Falta el nombre del pez", http.StatusBadRequest)
		return
	}

	if !esperarCatalogo() {
		http.Error(w, "Error al conectar con Xundra", http.StatusInternalServerError)
		return
	}

	partes := strings.SplitN(entradaUsuario, ":", 2)
	pezBase := strings.TrimSpace(partes[0])
	var varianteBuscada string
	if len(partes) > 1 {
		varianteBuscada = strings.TrimSpace(partes[1])
	}

	d, encontrado, err := obtenerDetalle(pezBase)
	if !encontrado {
		w.Write([]byte("❌ El objeto [" + pezBase + "] no está en el catálogo oficial."))
		return
	}
	if err != nil {
		http.Error(w, "Error al entrar al detalle", http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	for _, k := range d.orden {
		v := d.variantes[k]
		if varianteBuscada == "" || strings.EqualFold(v.Nombre, entradaUsuario) || strings.EqualFold(v.Nombre, varianteBuscada) {
			b.WriteString("• ")
			b.WriteString(v.Nombre)
			b.WriteString(" | EGGS: ")
			b.WriteString(v.EggsTxt)
			b.WriteString(" | FISH: ")
			b.WriteString(v.FishTxt)
			b.WriteString(" \n")
		}
	}

	if b.Len() == 0 {
		w.Write([]byte("Sin stock o variante no encontrada para: " + entradaUsuario))
		return
	}
	w.Write([]byte(b.String()))
}

// ===================================================
//  /api/pantalla
//  Entrada:  conteo = líneas "H|Especie: Variante|n" (huevo) o "F|Especie: Variante|n" (pez)
//                     (también acepta el formato antiguo "(Fish Egg) Especie: Variante|n")
//            huella = huella de los datos que la pantalla ya tiene (opcional)
//  Salida si nada cambió:   "IGUAL|hh:mm"
//  Salida si hay cambios:   "K|huella\nT|hh:mm\n" + datos de ancho fijo:
//     S|kpi1    kpi2    kpi3    kpi4                 (4 x 8, centrados; totales sobre TODAS)
//     R|<32: nombre recortado + " " + huevosGlob>   (más rara, hasta maxRare empates)
//     V|<32: nombre>|<8 hLoc>|<8 hGlob>|<8 pGlob>|<1 rara>   (máx. maxFilasPantalla)
//  503 si el catálogo no está listo.
// ===================================================
func pantallaHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	if !esperarCatalogo() {
		http.Error(w, "CARGANDO", http.StatusServiceUnavailable)
		return
	}

	r.ParseForm()
	conteo := r.FormValue("conteo")

	locales := make(map[string]*filaPantalla)
	var orden []string
	bases := make(map[string]bool)

	for _, linea := range strings.Split(conteo, "\n") {
		linea = strings.TrimSpace(linea)
		esHuevo := false

		// Formato comprimido: "H|..." o "F|..."
		if len(linea) > 2 && linea[1] == '|' && (linea[0] == 'H' || linea[0] == 'F') {
			esHuevo = linea[0] == 'H'
			linea = linea[2:]
		}

		sep := strings.LastIndexByte(linea, '|')
		if sep <= 0 {
			continue
		}

		nombre := strings.TrimSpace(linea[:sep])
		n := aEntero(linea[sep+1:])

		// Formato antiguo: "(Fish Egg) ..."
		if len(nombre) > len(prefijoHuevo) && strings.EqualFold(nombre[:len(prefijoHuevo)], prefijoHuevo) {
			nombre = strings.TrimSpace(nombre[len(prefijoHuevo):])
			esHuevo = true
		}

		partes := strings.SplitN(nombre, ":", 2)
		if len(partes) < 2 {
			continue
		}
		base := strings.ToLower(strings.TrimSpace(partes[0]))
		if !enCatalogo(base) {
			continue
		}

		clave := strings.ToLower(nombre)
		f, existe := locales[clave]
		if !existe {
			f = &filaPantalla{Nombre: nombre}
			locales[clave] = f
			orden = append(orden, clave)
		}
		if esHuevo {
			f.LocEggs += n
		} else {
			f.LocFish += n
		}
		bases[base] = true
	}

	resultados := detallesEnParalelo(bases)

	filas := make([]filaPantalla, 0, len(orden))
	for _, clave := range orden {
		f := locales[clave]
		base := strings.ToLower(strings.TrimSpace(strings.SplitN(clave, ":", 2)[0]))
		d, ok := resultados[base]
		if !ok {
			continue
		}
		v, oficial := d.variantes[clave]
		if !oficial {
			continue
		}
		f.Nombre = v.Nombre
		f.GlobEggs = v.Eggs
		f.GlobFish = v.Fish
		filas = append(filas, *f)
	}

	// Totales y mínimo global sobre TODAS las variantes, antes de recortar.
	var sumLE, sumGE, sumGF int
	minGE := -1
	for _, f := range filas {
		sumLE += f.LocEggs
		sumGE += f.GlobEggs
		sumGF += f.GlobFish
		if minGE < 0 || f.GlobEggs < minGE {
			minGE = f.GlobEggs
		}
	}

	sort.SliceStable(filas, func(i, j int) bool {
		if filas[i].LocEggs != filas[j].LocEggs {
			return filas[i].LocEggs > filas[j].LocEggs
		}
		if filas[i].LocFish != filas[j].LocFish {
			return filas[i].LocFish > filas[j].LocFish
		}
		return filas[i].Nombre < filas[j].Nombre
	})

	var d strings.Builder
	d.Grow(40 + (maxRare*36) + (maxFilasPantalla * 66))

	d.WriteString("S|")
	d.WriteString(centrar(strconv.Itoa(sumLE), 8))
	d.WriteString(centrar(strconv.Itoa(len(filas)), 8))
	d.WriteString(centrar(strconv.Itoa(sumGE), 8))
	d.WriteString(centrar(strconv.Itoa(sumGF), 8))
	d.WriteByte('\n')

	nRare := 0
	for _, f := range filas {
		if f.GlobEggs == minGE {
			num := " " + strconv.Itoa(f.GlobEggs)
			d.WriteString("R|")
			d.WriteString(padDer(recortar(f.Nombre, 32-len(num))+num, 32))
			d.WriteByte('\n')
			nRare++
			if nRare >= maxRare {
				break
			}
		}
	}

	if len(filas) > maxFilasPantalla {
		filas = filas[:maxFilasPantalla]
	}
	for _, f := range filas {
		d.WriteString("V|")
		d.WriteString(padDer(recortar(f.Nombre, 32), 32))
		d.WriteByte('|')
		d.WriteString(padIzq(strconv.Itoa(f.LocEggs), 8))
		d.WriteByte('|')
		d.WriteString(padIzq(strconv.Itoa(f.GlobEggs), 8))
		d.WriteByte('|')
		d.WriteString(padIzq(strconv.Itoa(f.GlobFish), 8))
		if f.GlobEggs == minGE {
			d.WriteString("|1\n")
		} else {
			d.WriteString("|0\n")
		}
	}

	datos := d.String()
	h := fnv.New32a()
	h.Write([]byte(datos))
	huella := strconv.FormatUint(uint64(h.Sum32()), 16)
	hora := time.Now().In(zonaSLT).Format("15:04")

	if r.FormValue("huella") == huella {
		w.Write([]byte("IGUAL|" + hora))
		return
	}
	w.Write([]byte("K|" + huella + "\nT|" + hora + "\n" + datos))
}

func main() {
	go iniciarActualizadorAutomatico()

	http.HandleFunc("/api/filtrar_menu", filtrarMenuHandler)
	http.HandleFunc("/api/pez", consultarExistenciasHandler)
	http.HandleFunc("/api/pantalla", pantallaHandler)

	puerto := os.Getenv("PORT")
	if puerto == "" {
		puerto = "8080"
	}

	srv := &http.Server{
		Addr:              ":" + puerto,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Println("Servidor Go optimizado activo en puerto " + puerto + "...")
	log.Fatal(srv.ListenAndServe())
}
