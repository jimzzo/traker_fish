package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	mutex          sync.RWMutex
	catalogoPeces  []string
	variantesCache = make(map[string][]string)
)

func actualizarCatalogo() {
	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		log.Println("⚠️ ERROR crítico al conectar con Xundra:", err)
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		log.Println("⚠️ ERROR leyendo HTML de Xundra:", err)
		return
	}

	var nuevosPeces []string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		nombrePez := strings.TrimSpace(s.Text())
		if nombrePez != "" {
			encontrado := false
			for _, p := range nuevosPeces {
				if strings.EqualFold(p, nombrePez) {
					encontrado = true
					break
				}
			}
			if !encontrado {
				nuevosPeces = append(nuevosPeces, nombrePez)
			}
		}
	})

	mutex.Lock()
	catalogoPeces = nuevosPeces
	variantesCache = make(map[string][]string)
	mutex.Unlock()
	log.Printf("✅ Catálogo base sincronizado con éxito: %d peces encontrados.", len(nuevosPeces))
}

func iniciarActualizadorAutomatico() {
	actualizarCatalogo()
	ticker := time.NewTicker(5 * time.Hour)
	go func() {
		for range ticker.C {
			actualizarCatalogo()
		}
	}()
}

func obtenerVariantesOficiales(pezBase string) []string {
	mutex.RLock()
	if vars, existe := variantesCache[pezBase]; existe {
		mutex.RUnlock()
		return vars
	}
	mutex.RUnlock()

	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	var linkDetalle string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		if strings.EqualFold(strings.TrimSpace(s.Text()), pezBase) {
			if href, existe := s.Attr("href"); existe {
				linkDetalle = href
			}
		}
	})

	if linkDetalle == "" {
		return nil
	}

	if !strings.HasPrefix(linkDetalle, "http") {
		linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
	}

	reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
	reqDetalle.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	respDetalle, err := client.Do(reqDetalle)
	if err != nil {
		return nil
	}
	defer respDetalle.Body.Close()

	docDetalle, _ := goquery.NewDocumentFromReader(respDetalle.Body)
	var variantes []string

	docDetalle.Find("tr").Each(func(j int, fila *goquery.Selection) {
		var celdas []string
		fila.Find("td").Each(func(k int, cell *goquery.Selection) {
			celdas = append(celdas, strings.TrimSpace(cell.Text()))
		})

		if len(celdas) >= 3 {
			nombreVariante := celdas[0]
			if nombreVariante != "" {
				encontrada := false
				for _, v := range variantes {
					if strings.EqualFold(v, nombreVariante) {
						encontrada = true
						break
					}
				}
				if !encontrada {
					variantes = append(variantes, nombreVariante)
				}
			}
		}
	})

	mutex.Lock()
	variantesCache[pezBase] = variantes
	mutex.Unlock()

	return variantes
}

func filtrarMenuHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	nombresRecibidos := r.FormValue("objetos")
	
	mutex.RLock()
	catalogoLocal := catalogoPeces
	mutex.RUnlock()

	// LOGS DE DEPURACIÓN EN RENDER
	log.Printf("📥 Recibido del HUD: [%s]", nombresRecibidos)
	log.Printf("📦 Peces base en memoria: %d", len(catalogoLocal))

	if nombresRecibidos == "" || len(catalogoLocal) == 0 {
		w.Write([]byte("VACIO"))
		return
	}

	objetosList := strings.Split(nombresRecibidos, "|||")
	var validos []string

OUTER:
	for _, obj := range objetosList {
		obj = strings.TrimSpace(obj)
		if obj == "" {
			continue
		}

		partesObj := strings.Split(obj, ":")
		pezBase := strings.TrimSpace(partesObj[0])

		pezBaseValido := false
		for _, cat := range catalogoLocal {
			if strings.EqualFold(pezBase, cat) {
				pezBaseValido = true
				break
			}
		}

		if !pezBaseValido {
			log.Printf("❌ Descartado (Pez base no existe en catálogo): %s", pezBase)
			continue
		}

		if len(partesObj) > 1 {
			varianteBuscada := strings.TrimSpace(partesObj[1])
			variantesOficiales := obtenerVariantesOficiales(pezBase)

			varianteReal := false
			for _, vOficial := range variantesOficiales {
				if strings.EqualFold(vOficial, varianteBuscada) {
					varianteReal = true
					break
				}
			}

			if !varianteReal {
				log.Printf("❌ Descartado (Variante falsa/inventada): %s", obj)
				continue
			}
		}

		for _, v := range validos {
			if strings.EqualFold(v, obj) {
				continue OUTER
			}
		}
		validos = append(validos, obj)
		log.Printf("✅ Aceptado como válido: %s", obj)
	}

	if len(validos) == 0 {
		w.Write([]byte("VACIO"))
		return
	}

	w.Write([]byte(strings.Join(validos, "\n")))
}

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

	partes := strings.Split(entradaUsuario, ":")
	pezBase := strings.TrimSpace(partes[0])
	var varianteBuscada string
	if len(partes) > 1 {
		varianteBuscada = strings.TrimSpace(partes[1])
	}

	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		http.Error(w, "Error al conectar con Xundra", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		http.Error(w, "Error leyendo HTML", http.StatusInternalServerError)
		return
	}

	var linkDetalle string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		if strings.EqualFold(strings.TrimSpace(s.Text()), pezBase) {
			if href, existe := s.Attr("href"); existe {
				linkDetalle = href
			}
		}
	})

	if linkDetalle == "" {
		w.Write([]byte(fmt.Sprintf("❌ El objeto [%s] no está en el catálogo oficial.", pezBase)))
		return
	}

	if !strings.HasPrefix(linkDetalle, "http") {
		linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
	}

	reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
	reqDetalle.Header.Set("User-Agent", "Mozilla/5.0")
	respDetalle, err := client.Do(reqDetalle)
	if err != nil {
		http.Error(w, "Error al entrar al detalle", http.StatusInternalServerError)
		return
	}
	defer respDetalle.Body.Close()

	docDetalle, err := goquery.NewDocumentFromReader(respDetalle.Body)
	if err != nil {
		http.Error(w, "Error leyendo detalle", http.StatusInternalServerError)
		return
	}

	var resultadoBuilder strings.Builder
	encontradoVariante := false

	docDetalle.Find("tr").Each(func(i int, s *goquery.Selection) {
		var celdas []string
		s.Find("td").Each(func(j int, cell *goquery.Selection) {
			celdas = append(celdas, strings.TrimSpace(cell.Text()))
		})

		if len(celdas) >= 3 {
			nombreFila := celdas[0]
			eggs := celdas[1]
			fish := celdas[2]

			if varianteBuscada == "" || strings.EqualFold(nombreFila, varianteBuscada) {
				resultadoBuilder.WriteString(fmt.Sprintf("• %s EGGS: %s | FISH: %s \n", nombreFila, eggs, fish))
				encontradoVariante = true
			}
		}
	})

	mensajeFinal := resultadoBuilder.String()
	if !encontradoVariante {
		mensajeFinal = fmt.Sprintf("Sin stock para: %s", entradaUsuario)
	}

	w.Write([]byte(mensajeFinal))
}

func main() {
	go iniciarActualizadorAutomatico()

	http.HandleFunc("/api/filtrar_menu", filtrarMenuHandler)
	http.HandleFunc("/api/pez", consultarExistenciasHandler)

	fmt.Println("Servidor Go optimizado activo en puerto 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
