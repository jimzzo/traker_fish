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
	mutex         sync.RWMutex
	catalogoPeces = make(map[string][]string) // Clave: Pez base, Valor: Lista de variantes oficiales
)

// Actualiza el catálogo y sus variantes oficiales cada 5 horas en segundo plano
func actualizarCatalogo() {
	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		log.Println("⚠️ Error al actualizar el catálogo:", err)
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return
	}

	nuevoCatalogo := make(map[string][]string)

	// Encontramos cada pez base y su enlace de detalle
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		pezBase := strings.TrimSpace(s.Text())
		if pezBase == "" {
			return
		}

		href, existe := s.Attr("href")
		if !existe {
			return
		}

		linkDetalle := href
		if !strings.HasPrefix(linkDetalle, "http") {
			linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
		}

		// Descargamos las variantes de este pez base para guardarlas en caché
		reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
		reqDetalle.Header.Set("User-Agent", "Mozilla/5.0")
		respDetalle, err := client.Do(reqDetalle)
		if err != nil {
			return
		}
		defer respDetalle.Body.Close()

		docDetalle, err := goquery.NewDocumentFromReader(respDetalle.Body)
		if err != nil {
			return
		}

		var variantes []string
		docDetalle.Find("tr").Each(func(j int, fila *goquery.Selection) {
			var celdas []string
			fila.Find("td").Each(func(k int, cell *goquery.Selection) {
				celdas = append(celdas, strings.TrimSpace(cell.Text()))
			})

			if len(celdas) >= 3 {
				nombreVariante := celdas[0]
				if nombreVariante != "" {
					// Evitamos duplicados de variantes
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

		// Guardamos el pez base con su lista oficial de variantes
		nuevoCatalogo[pezBase] = variantes
	})

	mutex.Lock()
	catalogoPeces = nuevoCatalogo
	mutex.Unlock()
	log.Printf("✅ Catálogo sincronizado: %d peces base con sus variantes en caché.", len(nuevoCatalogo))
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

// Endpoint 1: Filtro ultrarrápido que valida que tanto el pez base como la variante existan oficialmente
func filtrarMenuHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	nombresRecibidos := r.FormValue("objetos")
	if nombresRecibidos == "" {
		w.Write([]byte("VACIO"))
		return
	}

	objetosList := strings.Split(nombresRecibidos, "|||")
	var validos []string

	mutex.RLock()
	catalogoLocal := catalogoPeces
	mutex.RUnlock()

	for _, obj := range objetosList {
		obj = strings.TrimSpace(obj)
		if obj == "" {
			continue
		}

		// Separamos nombre base y variante (ej: "Crayfish: Prueba Hembra")
		partesObj := strings.Split(obj, ":")
		pezBaseEscaneado := strings.TrimSpace(partesObj[0])

		// 1. Buscamos el pez base en la caché ignorando mayúsculas/minúsculas
		var variantesOficiales []string
		pezBaseEncontrado := false
		for baseOficial, vars := range catalogoLocal {
			if strings.EqualFold(baseOficial, pezBaseEscaneado) {
				pezBaseEncontrado = true
				variantesOficiales = vars
				break
			}
		}

		if !pezBaseEncontrado {
			continue // El pez base no existe, descartado
		}

		// 2. Si el objeto tiene una variante especificada (ej: "Prueba Hembra"), comprobamos que sea real
		if len(partesObj) > 1 {
			varianteEscaneada := strings.TrimSpace(partesObj[1])
			varianteReal := false

			for _, vOficial := range variantesOficiales {
				if strings.EqualFold(vOficial, varianteEscaneada) {
					varianteReal = true
					break
				}
			}

			if !varianteReal {
				continue // Variante inventada (como "Prueba Hembra"), descartada automáticamente
			}
		}

		// Si pasa todos los filtros, lo añadimos al menú sin duplicados
		duplicado := false
		for _, v := range validos {
			if strings.EqualFold(v, obj) {
				duplicado = true
				break
			}
		}
		if !duplicado {
			validos = append(validos, obj)
		}
	}

	if len(validos) == 0 {
		w.Write([]byte("VACIO"))
		return
	}

	w.Write([]byte(strings.Join(validos, "\n")))
}

// Endpoint 2: Consulta las existencias detalladas de un pez específico al pulsar el botón
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
