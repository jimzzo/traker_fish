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
	catalogoPeces []string
)

// Actualiza el catálogo de la web oficial de Xundra cada 5 horas en segundo plano
func actualizarCatalogo() {
	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{Timeout: 10 * time.Second}
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
	mutex.Unlock()
	log.Printf("✅ Catálogo sincronizado en segundo plano: %d peces base.", len(nuevosPeces))
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

// Función auxiliar para comprobar si una variante existe realmente en la página de detalle del pez
func validarVarianteEnWeb(pezBase, varianteBuscada string) bool {
	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return false
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
		return false
	}

	if !strings.HasPrefix(linkDetalle, "http") {
		linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
	}

	reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
	reqDetalle.Header.Set("User-Agent", "Mozilla/5.0")
	respDetalle, err := client.Do(reqDetalle)
	if err != nil {
		return false
	}
	defer respDetalle.Body.Close()

	docDetalle, _ := goquery.NewDocumentFromReader(respDetalle.Body)
	encontrada := false

	docDetalle.Find("tr").Each(func(i int, s *goquery.Selection) {
		s.Find("td").Each(func(j int, cell *goquery.Selection) {
			textoCelda := strings.TrimSpace(cell.Text())
			if strings.EqualFold(textoCelda, varianteBuscada) {
				encontrada = true
			}
		})
	})

	return encontrada
}

// Endpoint 1: Filtro estricto que valida tanto el pez base como la variante exacta en la web
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

		// Separamos nombre base y variante (si tiene dos puntos)
		partesObj := strings.Split(obj, ":")
		pezBase := strings.TrimSpace(partesObj[0])

		// 1. Validar que el pez base exista en el catálogo general
		pezBaseValido := false
		for _, cat := range catalogoLocal {
			if strings.EqualFold(pezBase, cat) {
				pezBaseValido = true
				break
			}
		}

		if !pezBaseValido {
			continue // Si el pez base no existe, descartado
		}

		// 2. Si el usuario especificó una variante (ej: "Crayfish: Blue Spotted"), 
		// debemos comprobar que esa variante exista realmente en la web de Xundra.
		if len(partesObj) > 1 {
			varianteBuscada := strings.TrimSpace(partesObj[1])
			// Validamos contra la web si la variante es real (ej: rechaza "Prueba Hembra")
			if !validarVarianteEnWeb(pezBase, varianteBuscada) {
				continue // Variante falsa o inventada por usuario, descartada
			}
		}

		// Si pasa los filtros, lo añadimos sin duplicados
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

// Endpoint 2: Consulta las existencias detalladas de un pez específico (EGGS / FISH)
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
		w.Write([]byte(fmt.Sprintf("❌ El objeto [%s] no está en el catálogo.", pezBase)))
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

			if varianteBuscada == "" || strings.Contains(strings.ToLower(nombreFila), strings.ToLower(varianteBuscada)) {
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
